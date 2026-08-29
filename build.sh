#!/bin/bash
# 阶段1 构建脚本（BUILD.md）：Go 静态编译，零第三方依赖（B-1 / H-4）
# 用法：./build.sh              # 阶段1：构建 Linux 二进制
#        ./build.sh windows     # 阶段2：交叉编译 downloader.exe
set +e
cd /data/workspace
export PATH=$PATH:/usr/local/go/bin
export GOFLAGS=-mod=readonly
export GOPROXY=off
export CGO_ENABLED=0
: > build.log

TARGET="${1:-linux}"

echo "=== [B1] 环境断言 ===" | tee -a build.log
go version 2>&1 | tee -a build.log
uname -a | tee -a build.log
nproc | tee -a build.log

echo "=== [B2] 源码静态分析 (go vet) ===" | tee -a build.log
go vet ./... 2>&1 | tee -a build.log

echo "=== [B3] 单元测试 (竞态检测, -race) ===" | tee -a build.log
go test -race -v ./... 2>&1 | tee -a build.log
echo "test_exit=${PIPESTATUS[0]}" | tee -a build.log

echo "=== [B4] 静态编译 downloader ===" | tee -a build.log
mkdir -p bin
if [ "$TARGET" = "windows" ]; then
  export GOOS=windows GOARCH=amd64
  go build -ldflags="-s -w" -o bin/downloader.exe ./cmd/downloader 2>&1 | tee -a build.log
else
  go build -ldflags="-s -w" -o bin/downloader ./cmd/downloader 2>&1 | tee -a build.log
fi
echo "build_exit=${PIPESTATUS[0]}" | tee -a build.log

echo "=== [B5] 二进制信息 ===" | tee -a build.log
if [ "$TARGET" = "windows" ]; then
  ls -lh bin/downloader.exe 2>&1 | tee -a build.log
  echo "--- file 类型（若在 wine/兼容环境可用）---" | tee -a build.log
  file bin/downloader.exe 2>&1 | tee -a build.log
else
  file bin/downloader 2>&1 | tee -a build.log
  ls -lh bin/downloader 2>&1 | tee -a build.log
  echo "--- 动态依赖（应为空/仅 libc）---" | tee -a build.log
  ldd bin/downloader 2>&1 | tee -a build.log
fi

echo "=== [B6] CLI 冒烟 (--help / -h) ===" | tee -a build.log
if [ "$TARGET" != "windows" ]; then
  ./bin/downloader -h 2>&1 | tee -a build.log || true
fi
echo "DONE" | tee -a build.log
