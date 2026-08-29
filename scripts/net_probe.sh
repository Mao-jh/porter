#!/bin/bash
# 外网探测脚本（构建前 / 工具链导入后各运行一次）
# 按 R-2 / H-4：预期失败。仅探测回环与内网，绝不发起真实外网请求。
set -u
echo "=== 外网探测 @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

probe() {
  local desc="$1"; local target="$2"
  echo -n "[$desc] $target -> "
  timeout 5 bash -c "echo > /dev/tcp/$target/443" 2>/dev/null && echo "REACHABLE(违规!)" || echo "unreachable(符合预期)"
}

probe "https_port_127" "127.0.0.1"
probe "rfc5737_TEST_NET" "192.0.2.1"
probe "link_local" "169.254.0.1"

echo "=== 工具链版本 ==="
go version 2>/dev/null || echo "go: not found"
gcc --version 2>/dev/null | head -1 || echo "gcc: not found"
uname -a
