#!/bin/bash
# 工具链导入（B-1 协议）：一次性受限窗口内获取 Go，随后立即断网验证
# 策略优先级：① 本地预置包 > ② apt install > ③ 官方二进制下载
set +e
cd /data/workspace
LOG=toolchain_import.log
: > $LOG

echo "=== [1] 扫描本地预置 Go 包 ===" | tee -a $LOG
ls /data/inputs/ 2>/dev/null | grep -iE 'go.*linux|golang' | tee -a $LOG
find / -maxdepth 4 -iname 'go*.tar.gz' 2>/dev/null | tee -a $LOG

echo "=== [2] 尝试 apt-get install golang-go (受限窗口) ===" | tee -a $LOG
apt-get install -y golang-go 2>&1 | tail -20 | tee -a $LOG
which go && go version 2>&1 | tee -a $LOG

if ! which go >/dev/null 2>&1; then
  echo "=== [3] apt 失败，尝试官方二进制 (go.dev) ===" | tee -a $LOG
  ARCH=$(uname -m); case $ARCH in x86_64) GOARCH=amd64;; aarch64) GOARCH=arm64;; *) GOARCH=amd64;; esac
  URL="https://go.dev/dl/go1.22.5.linux-${GOARCH}.tar.gz"
  echo "URL=$URL" | tee -a $LOG
  curl -sSL -o go.tar.gz "$URL" 2>&1 | tail -5 | tee -a $LOG
  ls -lh go.tar.gz 2>&1 | tee -a $LOG
  if [ -s go.tar.gz ]; then
    rm -rf /usr/local/go
    tar -C /usr/local -xzf go.tar.gz 2>&1 | tee -a $LOG
    export PATH=$PATH:/usr/local/go/bin
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /root/.bashrc
    go version 2>&1 | tee -a $LOG
  fi
fi

echo "=== [4] 导入后断网验证（断言失败） ===" | tee -a $LOG
timeout 8 curl -sS -o /dev/null -w "go.dev:%{http_code}\n" https://go.dev/dl/ 2>&1 | tee -a $LOG
timeout 8 curl -sS -o /dev/null -w "github:%{http_code}\n" https://github.com 2>&1 | tee -a $LOG
echo "（注：构建/测试全程 GOPROXY=off，禁止业务依赖联网）" | tee -a $LOG
