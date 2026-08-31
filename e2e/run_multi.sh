#!/bin/bash
# 进程级多任务 + 全局限速 e2e：3 个任务并发下载同一服务端不同文件
set -e
cd "$(dirname "$0")"
BIN=../bin
DATA=mdata
rm -rf "$DATA" outdir ts3.log
mkdir -p "$DATA" outdir

# 预生成三个确定性内容文件（与 testserver.PatternFill 公式一致）
python - << 'PYEOF'
import os
BS = 65536
def gen(name, size):
    with open(os.path.join("mdata", name), "wb") as f:
        off = 0
        while off < size:
            n = min(BS, size - off)
            f.write(bytes((( (off+i)//BS )*131 + ((off+i) % BS)*7 + 13) % 251 for i in range(n)))
            off += n
gen("m1.bin", 32*1024*1024)
gen("m2.bin", 32*1024*1024)
gen("m3.bin", 16*1024*1024)
PYEOF

# 服务端用一次性名字监听（CreateFile 会截断同名文件，绝不能碰 m1/m2/m3）
"$BIN/testserver.exe" -dir "$DATA" -name dummy.do-not-use -size 1 > ts3.log 2>&1 &
TS_PID=$!
sleep 1
BASE=$(grep '^url=' ts3.log | cut -d= -f2- | sed 's|/file/.*||')
echo "base=$BASE"
python -c "
import hashlib,sys
for n in ('m1.bin','m2.bin','m3.bin'):
    h=hashlib.sha256()
    with open('mdata/'+n,'rb') as f:
        for c in iter(lambda: f.read(65536), b''): h.update(c)
    print(n, h.hexdigest())" > src_sha.txt
cat src_sha.txt

echo "=== [M1] 三任务并发 + -o 目录 + 全局限速 12MiB/s ==="
START=$(date +%s)
"$BIN/porter.exe" "$BASE/file/m1.bin" "$BASE/file/m2.bin" "$BASE/file/m3.bin" \
  -o outdir -limit $((12*1024*1024)) -verify sha256
END=$(date +%s)
echo "elapsed=$((END-START))s（3 文件共 80MiB@12MiBps 理论下限 ≥6.7s）"

echo "=== [M2] sha256 比对 ==="
python -c "
import hashlib
for n in ('m1.bin','m2.bin','m3.bin'):
    h=hashlib.sha256()
    with open('outdir/'+n,'rb') as f:
        for c in iter(lambda: f.read(65536), b''): h.update(c)
    print(n, h.hexdigest())" > out_sha.txt
if diff src_sha.txt out_sha.txt; then echo "MATCH: 三任务内容全部一致"; else echo "MISMATCH"; kill $TS_PID 2>/dev/null; exit 1; fi

echo "=== [M3] 单任务 + -H 透传冒烟 ==="
"$BIN/porter.exe" "$BASE/file/m3.bin" -H "X-Probe: 1" -o outdir/h.bin >/dev/null 2>&1 && echo "带 -H 下载 OK"
ls -la outdir/
kill $TS_PID 2>/dev/null || true
echo "=== MULTI E2E DONE ==="
