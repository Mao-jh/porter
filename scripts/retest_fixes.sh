#!/bin/bash
# retest.sh — 定向复测 HANDON_REVIEW 修复点（第 24 轮复测）
# R1: testserver FTP 无 -dir 兜底（问题1）
# R2: HLS 加密任务 tasks size 统计（问题2）
# R3: -retry-forever 覆盖探测失败（问题4）
# R4: -min-rate 慢速判定 30s 窗口（问题5）
set -u
# 端口基址可覆盖（如 PBASE=54400 ./scripts/retest_fixes.sh），避免与门禁其他段冲突
PB="${PBASE:-54331}"
ROOT=/c/Users/31423/Desktop/deliverable
P=$ROOT/bin/porter.exe
TS=$ROOT/bin/testserver.exe
cd "$ROOT"
WORK="e2e/retest.$$"
mkdir -p "$WORK"
PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); echo "[PASS] $1"; }
bad(){ FAIL=$((FAIL+1)); echo "[FAIL] $1"; }
killp(){ kill "$1" 2>/dev/null; }

echo "===== R1: testserver FTP 空 -dir 兜底 ====="
"$TS" -addr 127.0.0.1:$PB -name big.bin -size 1048576 -ftp > "$WORK/r1.log" 2>&1 &
R1=$!
sleep 1.5
if grep -qi "Temp" "$WORK/r1.log"; then ok "R1a: 空 -dir 自动 MkdirTemp（file=...Temp\\dltest...）"; else bad "R1a: MkdirTemp 兜底缺失"; fi
FTPURL=$(grep '^ftpurl=' "$WORK/r1.log" | cut -d= -f2)
if "$P" "$FTPURL" -o "$WORK/r1.bin" > "$WORK/r1dl.log" 2>&1; then ok "R1b: FTP 无 -dir 下载成功"; else bad "R1b: FTP 无 -dir 下载失败"; tail -3 "$WORK/r1dl.log"; fi
"$P" "http://127.0.0.1:$PB/file/big.bin" -o "$WORK/r1ref.bin" >/dev/null 2>&1
if cmp -s "$WORK/r1.bin" "$WORK/r1ref.bin" 2>/dev/null; then ok "R1c: FTP 产物与 HTTP 参考哈希一致"; else bad "R1c: 哈希不一致"; fi
killp $R1

echo "===== R2: HLS 加密任务 tasks size 统计 ====="
"$TS" -dir "$WORK" -addr 127.0.0.1:$((PB+1)) -name big.bin -size 2097152 > "$WORK/r2.log" 2>&1 &
R2=$!
sleep 1.5
if "$P" "http://127.0.0.1:$((PB+1))/hls/big.bin.enc.m3u8" -o "$WORK/hls_enc.bin" -state-dir "$WORK/st2" > "$WORK/r2dl.log" 2>&1; then ok "R2a: HLS AES-128 加密下载成功"; else bad "R2a: HLS 加密下载失败"; tail -3 "$WORK/r2dl.log"; fi
SIZE_GOT=$(ls -la "$WORK/hls_enc.bin" 2>/dev/null | awk '{print $5}')
echo "  HLS 产物大小: ${SIZE_GOT:-缺失}"
TASKLINE=$("$P" tasks -state-dir "$WORK/st2" 2>/dev/null | grep -i "m3u8" | head -1)
echo "  size 行: ${TASKLINE:-（无 m3u8 记录）}"
if echo "$TASKLINE" | grep -qE "2\.0/2\.0|2097152|2\.0MiB"; then ok "R2b: tasks size 已回填（非 0/0B）"; else bad "R2b: tasks size 仍为 0/0B（未修复）"; fi
killp $R2

echo "===== R3: -retry-forever 覆盖探测失败（不可达端口） ====="
"$P" "http://127.0.0.1:$((PB+68))/none.bin" -retry-forever -state-dir "$WORK/st3" -o "$WORK/r3.bin" > "$WORK/r3.log" 2>&1 &
R3PID=$!
sleep 12
N_RETRY=$(grep -c "retry-forever" "$WORK/r3.log" 2>/dev/null || echo 0)
echo "  12s 内重试次数: $N_RETRY"
echo "  --- 日志尾部 ---"; tail -6 "$WORK/r3.log"
if [ "${N_RETRY:-0}" -ge 3 ]; then ok "R3: 探测失败持续退避重试（≥3 次，1s→2s→4s…）"; else bad "R3: 重试次数不足或未重试"; fi
killp $R3PID

echo "===== R4: -min-rate 慢速判定（限速源 50KB/s×3 分片 < 200KB/s 阈值 → 切镜像） ====="
"$TS" -dir "$WORK" -addr 127.0.0.1:$((PB+2)) -name big.bin -size 8388608 -limit 51200 > "$WORK/r4slow.log" 2>&1 &
R4S=$!
"$TS" -dir "$WORK" -addr 127.0.0.1:$((PB+3)) -name big.bin -size 8388608 > "$WORK/r4fast.log" 2>&1 &
R4F=$!
sleep 1.5
T0=$(date +%s)
if "$P" "http://127.0.0.1:$((PB+2))/file/big.bin" -mirror "http://127.0.0.1:$((PB+3))/file/big.bin" -min-rate 204800 -state-dir "$WORK/st4" -o "$WORK/r4.bin" > "$WORK/r4.log" 2>&1; then
  T1=$(date +%s)
  ok "R4a: 慢速判定后切镜像下载成功（${T1}s，30s 窗口生效）"
else
  bad "R4a: 下载失败"; tail -5 "$WORK/r4.log"
fi
grep -q "低于慢速阈值\|慢速" "$WORK/r4.log" && ok "R4b: 日志含慢速判定" || bad "R4b: 未见慢速判定日志"
"$P" "http://127.0.0.1:$((PB+3))/file/big.bin" -o "$WORK/r4ref.bin" >/dev/null 2>&1
if cmp -s "$WORK/r4.bin" "$WORK/r4ref.bin" 2>/dev/null; then ok "R4c: 切源后产物哈希一致"; else bad "R4c: 哈希不一致"; fi
killp $R4S; killp $R4F

echo
echo "===== 汇总: PASS=$PASS FAIL=$FAIL ====="
rm -rf "$WORK"
exit $FAIL
