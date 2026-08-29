#!/bin/bash
# FTP 协议端到端：真实 exe 对真实 FTP 服务端（testserver -ftp）
# 覆盖：全量分片下载 sha256 闭环 / 强杀续传 sha256 闭环 / 回环强制 / 协议白名单
set -e
cd "$(dirname "$0")"
BIN=../bin
DATA=e2edata_ftp
OUT=out_ftp.bin
ST=statedir_ftp
rm -rf "$DATA" "$ST" out_ftp.bin* ts_ftp.log
mkdir -p "$DATA"

SIZE=$((64*1024*1024))  # 64 MiB

echo "=== [F1] 启动 testserver.exe -ftp（HTTP+FTP 同目录 64MiB 模式文件；每连接限速 2MiB/s，6 分片聚合 ≈12MiB/s，保证 1.5s 中断确定性） ==="
"$BIN/testserver.exe" -ftp -limit 2097152 -dir "$DATA" -name big.bin -size $SIZE > ts_ftp.log 2>&1 &
TS_PID=$!
sleep 1
cat ts_ftp.log
FTP_URL=$(grep '^ftpurl=' ts_ftp.log | cut -d= -f2-)
[ -n "$FTP_URL" ] || { echo "未取得 ftpurl"; exit 1; }
echo "FTP_URL=$FTP_URL"

echo "=== [F2] 源文件 sha256 ==="
SRC_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$DATA/big.bin','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "src_sha256=$SRC_SHA"

echo "=== [F3] porter.exe FTP 全量下载（多分片并行 + sha256 校验） ==="
"$BIN/porter.exe" "$FTP_URL" -o "$OUT" -state-dir "$ST" -verify sha256

echo "=== [F4] 下载结果 sha256 比对 ==="
OUT_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$OUT','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "out_sha256=$OUT_SHA"
[ "$SRC_SHA" = "$OUT_SHA" ] && echo "MATCH: FTP 全量下载内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [F5] FTP 中断续传：启动 1.5s 后强杀进程，重启续传 ==="
rm -f "$OUT" "$OUT.part"; rm -rf "$ST"
"$BIN/porter.exe" "$FTP_URL" -o "$OUT" -state-dir "$ST" &
DL_PID=$!
sleep 1.5
kill -9 $DL_PID 2>/dev/null || taskkill //F //PID $DL_PID 2>/dev/null || true
sleep 0.5
echo "--- 中断后现场（.part 应存在且非全量） ---"
ls -la "$OUT.part" 2>/dev/null || { echo ".part 缺失!"; exit 1; }
python -c "
import json
st=json.load(open(r'$ST/state.json'))
k=list(st.keys())[0]
v=st[k]
print('status=',v['status'],'file_size=',v['file_size'],'done=',v['done'])
print('shards=',[(s['start'],s['end'],s['done']) for s in v.get('shards',[])])
" 2>/dev/null || echo "(state.json 读取失败)"

echo "--- 重启续传 ---"
"$BIN/porter.exe" "$FTP_URL" -o "$OUT" -state-dir "$ST" -verify sha256
RESUME_SHA=$(python -c "
import hashlib
h=hashlib.sha256()
with open(r'$OUT','rb') as f:
    for chunk in iter(lambda: f.read(65536), b''): h.update(chunk)
print(h.hexdigest())")
echo "resumed_sha256=$RESUME_SHA"
[ "$SRC_SHA" = "$RESUME_SHA" ] && echo "MATCH: FTP 续传后内容一致" || { echo "MISMATCH"; exit 1; }

echo "=== [F6] 安全冒烟：非回环 FTP 拒绝 / 未知协议拒绝 ==="
"$BIN/porter.exe" ftp://10.0.0.1/x 2>&1; echo "non-loopback ftp exit=$? (期望1/2)"
"$BIN/porter.exe" gopher://127.0.0.1/x 2>&1; echo "gopher exit=$? (期望2)"

kill $TS_PID 2>/dev/null || taskkill //F //PID $TS_PID 2>/dev/null || true
rm -rf "$DATA" "$ST" out_ftp.bin*
echo "=== FTP E2E DONE ==="
