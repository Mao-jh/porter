#!/bin/bash
# 穷尽探测所有可能的工具链路径（如实记录）
set +e
cd /data/workspace
echo "=== 编译器 ==="
for c in gccgo gcc clang tcc musl-gcc; do which $c 2>/dev/null && $c --version 2>&1 | head -1; done
echo "--- gcc libs ---"
ls /usr/lib/gcc 2>/dev/null; ls /usr/lib/x86_64-linux-gnu/ 2>/dev/null | grep -iE 'go|gcc' | head
echo "=== 其他语言运行时（可能自带编译能力）==="
for r in python3 node java rustc cargo; do which $r 2>/dev/null && $r --version 2>&1 | head -1; done
echo "=== 白名单探测：哪些域名可达 ==="
for h in https://golang.org/dl/ https://storage.googleapis.com https://mirrors.tencent.com https://mirrors.aliyun.com https://mirrors.tencent.com/golang/; do
  code=$(timeout 8 curl -sS -o /dev/null -w "%{http_code}" "$h" 2>/dev/null)
  echo "$h -> $code"
done
echo "=== 本地 apt 可用包（golang 相关）==="
apt-cache search golang 2>/dev/null | head
echo "=== pip / 可用包管理 ==="
which pip3 pip 2>/dev/null
ls /var/cache/apt/archives/*.deb 2>/dev/null | head -20
