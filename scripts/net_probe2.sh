#!/bin/bash
# 修正版外网探测 —— 区分「内网可达(正常)」与「真实公网可达(违规)」
# 按 R-2/H-4：所有 socket 仅 127.0.0.0/8，探测外网必须失败。
set -u
echo "=== 外网探测(修正) @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

# 真实公网探测目标（按 H-4 应全部失败）
for host in "8.8.8.8" "1.1.1.1" "github.com"; do
  echo -n "  public $host:443 -> "
  timeout 4 bash -c "echo > /dev/tcp/$host/443" 2>/dev/null && echo "REACHABLE(违反H-4)" || echo "unreachable(OK)"
done

# DNS 解析（应失败）
echo -n "  dns resolve github.com -> "
timeout 4 getent hosts github.com 2>/dev/null && echo "RESOLVED(违反H-4)" || echo "failed(OK)"

# 回环自检（应成功，证明网络栈本身可用）
echo -n "  loopback 127.0.0.1:443 -> "
timeout 3 bash -c "echo > /dev/tcp/127.0.0.1/443" 2>/dev/null && echo "open" || echo "closed(OK,无服务监听)"

echo "=== Go 工具链 ==="
command -v go && go version || echo "go: NOT INSTALLED（见 §未覆盖声明）"
echo "=== 构建约束 ==="
echo "GOFLAGS=-mod=readonly  GOPROXY=off  CGO_ENABLED=0"
