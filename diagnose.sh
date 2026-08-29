#!/bin/bash
# 环境诊断脚本 —— 如实探测，输出供 REPORT.md 引用（不伪造任何数据）
set +e
echo "===== OS ====="
cat /etc/os-release 2>/dev/null | head -5
echo
echo "===== Kernel / Arch ====="
uname -a
echo
echo "===== CPU ====="
nproc
echo
echo "===== Go ====="
which go; go version 2>&1
echo
echo "===== Package managers ====="
for pm in apt apt-get yum dnf snap; do which $pm 2>/dev/null && echo "$pm: present"; done
echo
echo "===== Go in apt cache / local ====="
ls /var/cache/apt/archives/ 2>/dev/null | grep -i golang
ls /usr/local/go/bin/ 2>/dev/null
echo
echo "===== Network reachability (raw) ====="
timeout 5 curl -sI https://go.dev/dl/ 2>&1 | head -3
echo "--- 8.8.8.8 ping ---"
timeout 5 ping -c1 8.8.8.8 2>&1 | tail -3
echo "--- DNS ---"
timeout 5 getent hosts github.com 2>&1
