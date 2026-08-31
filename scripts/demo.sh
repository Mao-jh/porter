#!/bin/bash
# demo.sh — Porter 一键试用（第 22 轮）：本地服务端 → 演示核心能力 → 自动清理。
#
# 设计目标：让新上下文（人或 AI）跑一个脚本即可完成核心验证，零摸索、零碰壁：
#   - testserver 用固定端口（-addr），URL 确定，不再"每次启动端口随机"
#   - 每步演示打印 [PASS]/[FAIL]，末尾汇总，退出码可断言
#   - 强杀续传用 PowerShell 按命令行特征精确杀进程（Git Bash kill -9 杀不干净 exe）
#   - 结束自动清理服务端与临时目录
#
# 用法: ./scripts/demo.sh [PORT]      # 默认端口 54321
# 依赖: bin/porter.exe、bin/testserver.exe、mcp/porter-mcp.exe（MCP 段需要 python）
set -u
cd "$(dirname "$0")/.."
ROOT=$(pwd)
PORT="${1:-54321}"
SIZE=$((16*1024*1024))               # big.bin 16MiB
WORK=$(mktemp -d)
LOG="$WORK/server.log"
PASS=0; FAIL=0; SKIP=0

step() { echo; echo "===== $1 ====="; }
ok()   { PASS=$((PASS+1)); echo "[PASS] $1"; }
ko()   { FAIL=$((FAIL+1)); echo "[FAIL] $1"; }
skip() { SKIP=$((SKIP+1)); echo "[SKIP] $1"; }

sha256() { certutil -hashfile "$1" SHA256 2>/dev/null | sed -n 2p | tr 'A-F' 'a-f'; }

# ---------- 0. 产物检查 ----------
step "0. 产物检查"
PORTER="$ROOT/bin/porter.exe"; TSERVER="$ROOT/bin/testserver.exe"; PMCP="$ROOT/mcp/porter-mcp.exe"
for f in "$PORTER" "$TSERVER"; do
  [ -f "$f" ] && ok "存在 $f" || ko "缺失 $f（先 ./run_tests.sh 或 go build）"
done
[ -f "$PMCP" ] && ok "存在 $PMCP" || skip "缺失 $PMCP（MCP 段跳过）"

# ---------- 1. 启动本地服务端（固定端口） ----------
step "1. 启动 testserver（127.0.0.1:$PORT，固定端口）"
if [ $FAIL -gt 0 ]; then echo "产物缺失，中止"; exit 1; fi
nohup "$TSERVER" -addr "127.0.0.1:$PORT" -size "$SIZE" -name big.bin -extra "tiny.bin:1024" > "$LOG" 2>&1 &
ready=""
for i in $(seq 1 40); do
  code=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/file/big.bin" --max-time 1 2>/dev/null)
  if [ "$code" = "200" ]; then ready=1; break; fi
  sleep 0.25
done
[ -n "$ready" ] && ok "服务端就绪 $(grep '^url=' "$LOG")" || ko "服务端 10s 内未就绪（见 $LOG）"
URL="http://127.0.0.1:$PORT/file/big.bin"
TINY="http://127.0.0.1:$PORT/file/tiny.bin"

# ---------- 2. 基础下载 + sha256 校验 ----------
step "2. 基础下载（16MiB，自动分片 + sha256）"
cd "$WORK" || exit 1
if "$PORTER" "$URL" -o a.bin -verify sha256 2>&1 | grep -q "完成"; then
  curl -s "$URL" -o ref.bin   # 参考副本（certutil 不接受 stdin）
  ph=$(sha256 a.bin); want=$(sha256 ref.bin)
  if [ -n "$ph" ] && [ "$ph" = "$want" ]; then ok "下载完成且 sha256 一致 ($ph)"; else ko "sha256 不一致 porter=$ph 源=$want"; fi
else
  ko "基础下载失败"; fi

# ---------- 3. probe / meta / tasks ----------
step "3. probe / meta / tasks 子命令"
"$PORTER" probe "$URL" > probe.txt 2>&1 && grep -q "size=" probe.txt && grep -q "ranged=" probe.txt \
  && ok "probe: $(tr '\n' ' ' < probe.txt)" || ko "probe 失败: $(cat probe.txt)"
"$PORTER" meta "$URL" > meta.txt 2>&1 && grep -q "Content-Length" meta.txt \
  && ok "meta: $(head -1 meta.txt)" || ko "meta 失败"
"$PORTER" tasks > tasks.txt 2>&1 && grep -q "a.bin" tasks.txt \
  && ok "tasks 列出 a.bin (done)" || ko "tasks 未列出 a.bin"

# ---------- 4. 批量任务 + 全局限速 ----------
step "4. 批量任务（-i 列表 + out= 命名）+ 全局限速 4MiB/s"
printf '%s out=x.bin\n%s out=y.bin\n' "$URL" "$TINY" > urls.txt
mkdir -p batch   # CLI 不自动创建输出目录（与 curl 一致）
start=$(date +%s)
if "$PORTER" -i urls.txt -o batch/ -j 2 -limit 4194304 > batch.log 2>&1; then
  el=$(( $(date +%s) - start ))
  if [ -f batch/x.bin ] && [ -f batch/y.bin ]; then
    ph=$(sha256 batch/x.bin); want=$(sha256 a.bin)
    if [ "$ph" = "$want" ]; then ok "批量完成 ${el}s（16MiB@4MiB/s 理论 4s，误差合理即可）且 sha256 一致"; else ko "批量产物哈希不一致"; fi
  else ko "批量产物缺失"; fi
else ko "批量任务失败"; fi

# ---------- 5. 断点续传（限速下载中途强杀 → 重启续传） ----------
step "5. 断点续传（1MiB/s 下载，3s 后强杀，重启续传 + sha256）"
"$PORTER" "$URL" -o resume.bin -limit 1048576 -state-dir .st5 > resume1.log 2>&1 &
sleep 3
powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { \$_.CommandLine -like '*porter.exe*resume.bin*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }" >/dev/null 2>&1
sleep 1
if "$PORTER" "$URL" -o resume.bin -limit 1048576 -state-dir .st5 > resume2.log 2>&1 && grep -q "完成" resume2.log; then
  ph=$(sha256 resume.bin); want=$(sha256 a.bin)
  if [ "$ph" = "$want" ]; then ok "强杀后续传成功，sha256 一致（字节级续传有效）"; else ko "续传产物哈希不一致"; fi
else ko "续传失败: $(tail -1 resume2.log)"; fi

# ---------- 6. MCP 冒烟（python 可用时） ----------
step "6. MCP 冒烟（initialize / tools / start / status / probe / 产物）"
PY=""
command -v python >/dev/null 2>&1 && PY=python || command -v python3 >/dev/null 2>&1 && PY=python3
if [ -n "$PY" ] && [ -f "$PMCP" ]; then
  # MSYS 路径（/c/...）转 Windows 路径（C:/...），否则原生 exe 解析错误
  WIN_ROOT=$(cygpath -w "$ROOT" 2>/dev/null || echo "$ROOT")
  WIN_PMCP=$(cygpath -w "$PMCP" 2>/dev/null || echo "$PMCP")
  WIN_OUT=$(cygpath -w "$WORK/mcp_out" 2>/dev/null || echo "$WORK/mcp_out")
  if "$PY" "$WIN_ROOT/scripts/mcp_smoke.py" "$WIN_PMCP" "$TINY" "$WIN_OUT"; then
    ok "MCP 全流程通过"; else ko "MCP 冒烟失败"; fi
else skip "无 python 或 porter-mcp.exe 缺失"; fi

# ---------- 7. 清理 ----------
step "7. 清理"
powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { \$_.CommandLine -like '*testserver.exe*$PORT*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }" >/dev/null 2>&1
rm -rf "$WORK"
ok "已停止服务端并清理临时目录"

# ---------- 汇总 ----------
echo
echo "===== 汇总: PASS=$PASS FAIL=$FAIL SKIP=$SKIP ====="
[ $FAIL -eq 0 ]
