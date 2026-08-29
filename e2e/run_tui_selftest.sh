#!/bin/bash
# TUI 无头自检 e2e：--selftest 模式对真实 testserver 全自动验收（无人眼）
set -e
cd "$(dirname "$0")"
BIN=../tui
TS=../bin
DATA=tdata
rm -rf "$DATA" outdir tst1 tst2 tui.log
mkdir -p "$DATA" outdir

SIZE=$((48*1024*1024))
"$TS/testserver.exe" -dir "$DATA" -name f.bin -size $SIZE -limit $((2*1024*1024)) > tui.log 2>&1 &
TS_PID=$!
sleep 1
BASE=$(grep '^url=' tui.log | cut -d= -f2- | sed 's|/file/.*||')
SRC_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open('tdata/f.bin','rb') as f:
    for c in iter(lambda: f.read(65536), b''): h.update(c)
print(h.hexdigest())")
URL="$BASE/file/f.bin"
echo "url=$URL src_sha=$SRC_SHA"

echo "=== [T1] selftest 全量下载（48MiB，服务端限速 2MiB/s/连接） ==="
"$BIN/porter-tui.exe" -selftest -url "$URL" -out outdir -state-dir tst1 -verify sha256
[ -f outdir/f.bin ] && echo "输出文件存在: $(stat -c%s outdir/f.bin) 字节"
OUT_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open('outdir/f.bin','rb') as f:
    for c in iter(lambda: f.read(65536), b''): h.update(c)
print(h.hexdigest())")
[ "$SRC_SHA" = "$OUT_SHA" ] && echo "MATCH: sha256 一致" || { echo "MISMATCH"; kill $TS_PID 2>/dev/null; exit 1; }

echo "=== [T2] 中断续传：限速下 1.5s 强杀 TUI 进程 → 重启 selftest 续传 ==="
rm -rf tst2; mkdir -p outdir2
"$BIN/porter-tui.exe" -selftest -url "$URL" -out outdir2 -state-dir tst2 -verify sha256 &
DPID=$!
sleep 1.5
kill -9 $DPID 2>/dev/null || taskkill //F //PID $DPID 2>/dev/null || true
sleep 0.5
PART=$(find outdir2 -name "f.bin.part" 2>/dev/null | head -1)
if [ -n "$PART" ]; then
  echo ".part 存在: $(stat -c%s "$PART") 字节（预分配）"
else
  echo "(无 .part：下载已提前完成，跳过续传场景)"
fi
python -c "
import json,glob
for p in glob.glob('tst2/*/state.json'):
    st=json.load(open(p))
    for k,v in st.items():
        print('resume_state: done=',v['done'],'/',v['file_size'],'status=',v['status'])" 2>/dev/null || true
"$BIN/porter-tui.exe" -selftest -url "$URL" -out outdir2 -state-dir tst2 -verify sha256
OUT2=$(python -c "
import hashlib
h=hashlib.sha256()
with open('outdir2/f.bin','rb') as f:
    for c in iter(lambda: f.read(65536), b''): h.update(c)
print(h.hexdigest())")
[ "$SRC_SHA" = "$OUT2" ] && echo "MATCH: 续传后 sha256 一致" || { echo "MISMATCH"; kill $TS_PID 2>/dev/null; exit 1; }

kill $TS_PID 2>/dev/null || true
echo "=== TUI SELFTEST E2E DONE ==="
