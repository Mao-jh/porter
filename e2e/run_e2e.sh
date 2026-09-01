#!/bin/bash
# 端到端运行时测试：真实 exe 对真实服务端
set -e
cd "$(dirname "$0")"
BIN=../bin
DATA=e2edata
OUT=out.bin
ST=statedir
rm -rf "$DATA" "$ST" out.bin* ts.log 2>/dev/null || true
mkdir -p "$DATA"

SIZE=$((64*1024*1024))  # 64 MiB

echo "=== [E1] 启动 testserver.exe（64MiB 模式文件） ==="
"$BIN/testserver.exe" -dir "$DATA" -name big.bin -size $SIZE > ts.log 2>&1 &
TS_PID=$!
# 轮询等待 url 就绪（Windows 下进程首次启动可能超过 1s，固定 sleep 会偶发失败）
URL=""
for _ in $(seq 1 20); do
  URL=$(grep '^url=' ts.log 2>/dev/null | cut -d= -f2-)
  [ -n "$URL" ] && break
  sleep 0.5
done
cat ts.log
[ -n "$URL" ] || { echo "未取得 URL"; kill $TS_PID 2>/dev/null || true; exit 1; }

echo "=== [E2] 源文件 sha256（python 流式计算） ==="
SRC_SHA=$(python -c "
import hashlib,sys
h=hashlib.sha256()
with open(r'$DATA/big.bin','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "src_sha256=$SRC_SHA"

echo "=== [E3] porter.exe 全量下载（自动分片 + sha256 校验） ==="
"$BIN/porter.exe" "$URL" -o "$OUT" -state-dir "$ST" -verify sha256
echo "exit=$?"

echo "=== [E4] 下载结果 sha256 比对 ==="
OUT_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$OUT','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "out_sha256=$OUT_SHA"
[ "$SRC_SHA" = "$OUT_SHA" ] && echo "MATCH: 全量下载内容一致" || { echo "MISMATCH"; exit 1; }
ls -la "$OUT"; [ ! -f "$OUT.part" ] && echo ".part 已清理"

echo "=== [E5] 中断续传：启动 1.5s 后强杀进程，重启续传 ==="
rm -f "$OUT" "$OUT.part" 2>/dev/null || true; rm -rf "$ST" 2>/dev/null || true
"$BIN/porter.exe" "$URL" -o "$OUT" -state-dir "$ST" &
DL_PID=$!
sleep 1.5
kill -9 $DL_PID 2>/dev/null || taskkill //F //PID $DL_PID 2>/dev/null || true
sleep 0.5
echo "--- 中断后现场 ---"
ls -la "$OUT".part "$ST" 2>/dev/null || echo "(.part/state 缺失!)"
python -c "
import json
st=json.load(open(r'$ST/state.json'))
k=list(st.keys())[0]
v=st[k]
print('status=',v['status'],'file_size=',v['file_size'],'done=',v['done'])
print('shards=',[(s['start'],s['end'],s['done']) for s in v.get('shards',[])])
" 2>/dev/null || echo "(state.json 读取失败)"

echo "--- 重启续传 ---"
"$BIN/porter.exe" "$URL" -o "$OUT" -state-dir "$ST" -verify sha256
RESUME_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$OUT','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "resumed_sha256=$RESUME_SHA"
[ "$SRC_SHA" = "$RESUME_SHA" ] && echo "MATCH: 续传后内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [E6] CLI 参数/退出码冒烟 ==="
set +e  # 断言段：故意失败命令需打印退出码，不能触发 set -e
"$BIN/porter.exe" 2>&1; echo "no-args exit=$? (期望2)"
"$BIN/porter.exe" "$URL" -mode bogus 2>&1 | head -2; echo "---"
"$BIN/porter.exe" http://10.0.0.1/x 2>&1; echo "non-loopback exit=$? (期望1/2)"

set -e  # 断言段结束，恢复严格模式
kill $TS_PID 2>/dev/null || taskkill //F //PID $TS_PID 2>/dev/null || true
echo "=== E2E DONE ==="
