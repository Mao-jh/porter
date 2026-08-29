#!/bin/bash
# 协议扩展端到端（第 13 轮）：file:// / HLS(明文+AES-128+主列表) / Metalink4
# 全部走真实 porter.exe 对真实 testserver，sha256 闭环 + 负例（坏哈希/直播流）
set -e
cd "$(dirname "$0")"
BIN=../bin
DATA=e2edata_proto
rm -rf "$DATA" out_file.bin* out_hls.bin* out_enc.bin* out_master.bin* out_meta4*.bin* ts_proto.log porter-meta4-out.bin*
mkdir -p "$DATA"

SIZE=$((8*1024*1024))  # 8 MiB → 8 个 1MiB 段（虚拟映射 ≥5MiB → 并行分片路径）

echo "=== [P0] 启动 testserver.exe（HTTP + HLS + Metalink 端点；附 tiny.bin 供主列表低码率变体） ==="
"$BIN/testserver.exe" -dir "$DATA" -name big.bin -size $SIZE -extra "tiny.bin:1048576" > ts_proto.log 2>&1 &
TS_PID=$!
trap 'kill $TS_PID 2>/dev/null || true' EXIT
sleep 1
cat ts_proto.log
BASE=$(grep '^url=' ts_proto.log | cut -d= -f2- | sed 's|/file/big.bin$||')
[ -n "$BASE" ] && [ "$BASE" != "http://" ] || { echo "未取得 baseURL"; exit 1; }
echo "BASE=$BASE"

echo "=== [P1] 源文件 sha256 ==="
SRC_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$DATA/big.bin','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "src_sha256=$SRC_SHA"

sha_of() {
python -c "
import hashlib
h=hashlib.sha256()
with open(r'$1','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())"
}

echo "=== [P2] file:// 全量下载（本地复制语义 + sha256 校验） ==="
FILE_URL="file:///$(python -c "import os,sys;print(os.path.abspath('$DATA/big.bin').replace('\\\\','/'))")"
"$BIN/porter.exe" "$FILE_URL" -o out_file.bin -state-dir st_proto -verify sha256
[ "$(sha_of out_file.bin)" = "$SRC_SHA" ] && echo "MATCH: file:// 内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [P3] HLS 明文媒体播放列表（虚拟映射 → 并行分片 + sha256） ==="
"$BIN/porter.exe" "$BASE/hls/big.bin.m3u8" -o out_hls.bin -state-dir st_proto -verify sha256
[ "$(sha_of out_hls.bin)" = "$SRC_SHA" ] && echo "MATCH: HLS 明文内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [P4] HLS AES-128 加密播放列表（顺序流式解密 + sha256） ==="
"$BIN/porter.exe" "$BASE/hls/big.bin.enc.m3u8" -o out_enc.bin -state-dir st_proto -verify sha256
[ "$(sha_of out_enc.bin)" = "$SRC_SHA" ] && echo "MATCH: AES-128 解密内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [P5] HLS 主播放列表（应自动选最高码率变体 = big.bin） ==="
"$BIN/porter.exe" "$BASE/hls/big.bin.master.m3u8" -o out_master.bin -state-dir st_proto -verify sha256
[ "$(sha_of out_master.bin)" = "$SRC_SHA" ] && echo "MATCH: 主列表选中高码率变体" || { echo "MISMATCH"; exit 1; }

echo "=== [P6] Metalink4：priority=1 候选 404 → 自动 failover 到 priority=2 + 元数据哈希校验 ==="
"$BIN/porter.exe" "$BASE/meta4/big.bin.meta4" -o out_meta4.bin -state-dir st_proto
[ "$(sha_of out_meta4.bin)" = "$SRC_SHA" ] && echo "MATCH: Metalink failover 内容一致（显式 -o 尊重用户命名）" || { echo "MISMATCH"; exit 1; }

echo "=== [P6b] 无 -o 时输出名取自 <file name> 属性 ==="
"$BIN/porter.exe" "$BASE/meta4/big.bin.meta4" -state-dir st_proto
[ -f porter-meta4-out.bin ] && echo "OK: 输出名取自 <file name> 属性" || { echo "缺少 porter-meta4-out.bin"; exit 1; }
[ "$(sha_of porter-meta4-out.bin)" = "$SRC_SHA" ] && echo "MATCH: 内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [P7] 负例：Metalink 哈希不符 → 任务失败且产物被删除 ==="
set +e
"$BIN/porter.exe" "$BASE/meta4/big.bin.bad.meta4" -o out_badhash.bin -state-dir st_proto > badhash.log 2>&1
RC=$?
set -e
echo "exit=$RC"
[ $RC -ne 0 ] || { echo "坏哈希应失败"; exit 1; }
grep -q "哈希不一致" badhash.log || { echo "应报哈希不一致"; exit 1; }
[ ! -f out_badhash.bin ] || { echo "失败产物应被删除"; exit 1; }
echo "OK: 期望值校验拒绝损坏产物"

echo "=== [P8] 负例：直播流（无 ENDLIST）→ 拒绝 ==="
set +e
"$BIN/porter.exe" "$BASE/hls/big.bin.live.m3u8" -o out_live.bin -state-dir st_proto > live.log 2>&1
RC=$?
set -e
echo "exit=$RC"
[ $RC -ne 0 ] || { echo "直播流应失败"; exit 1; }
grep -q "ENDLIST" live.log || { echo "应提示 ENDLIST"; exit 1; }
echo "OK: 直播流被拒绝（任务有限性合规边界）"

echo "=== 协议端到端全部通过 ==="
