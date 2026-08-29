#!/bin/bash
# 进程级中断续传测试：限速服务端 + 强杀 + 重启续传
set -e
cd "$(dirname "$0")"
BIN=../bin
DATA=rdata
OUT=r.bin
ST=rst
rm -rf "$DATA" "$ST" r.bin* ts2.log
mkdir -p "$DATA"

SIZE=$((64*1024*1024))   # 64 MiB
LIMIT=$((4*1024*1024))   # 每连接 4 MiB/s，3 分片聚合约 12 MiB/s → 全程约 5.5s

"$BIN/testserver.exe" -dir "$DATA" -name big.bin -size $SIZE -limit $LIMIT > ts2.log 2>&1 &
TS_PID=$!
sleep 1
URL=$(grep '^url=' ts2.log | cut -d= -f2-)
SRC_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$DATA/big.bin','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "src_sha256=$SRC_SHA  url=$URL"

echo "=== [R1] 启动下载，1.5s 后强杀（预期约完成 20-30MiB） ==="
"$BIN/porter.exe" "$URL" -o "$OUT" -state-dir "$ST" &
DL_PID=$!
sleep 1.5
kill -9 $DL_PID 2>/dev/null || taskkill //F //PID $DL_PID 2>/dev/null || true
sleep 0.5
PART=${OUT}.part
if [ -f "$PART" ]; then
  echo ".part 存在: $(stat -c%s "$PART") 字节（预分配）"
else
  echo "(无 .part —— 下载可能已完成，调大文件或限速更低)"; kill $TS_PID 2>/dev/null; exit 1
fi
python -c "
import json
st=json.load(open(r'$ST/state.json'))
k=list(st.keys())[0]; v=st[k]
print('status=',v['status'],' done=',v['done'],'/',v['file_size'])
print('shards=',[(s['start'],s['end'],s['done']) for s in v.get('shards',[])])
"

echo "=== [R2] 重启续传至完成 ==="
"$BIN/porter.exe" "$URL" -o "$OUT" -state-dir "$ST" -verify sha256
GOT=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$OUT','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "resumed_sha256=$GOT"
if [ "$SRC_SHA" = "$GOT" ]; then echo "MATCH: 进程强杀后续传内容一致"; else echo "MISMATCH"; kill $TS_PID 2>/dev/null; exit 1; fi

echo "=== [R3] 再次续传幂等：重复运行同参数（state=done → 全新重下成功） ==="
"$BIN/porter.exe" "$URL" -o "$OUT" -state-dir "$ST" >/dev/null 2>&1 && echo "re-run exit=0"

kill $TS_PID 2>/dev/null || true
echo "=== RESUME E2E DONE ==="
