#!/bin/bash
# 开源合规自动检查（第 13 轮）：
#   1. 核心模块零第三方依赖断言（go list -m all 仅本模块）
#   2. LICENSE 存在且为 MIT、持有人与仓库一致
#   3. 遥测关键词扫描（核心源码不得出现 telemetry/analytics 上报语义）
#   4. UA 自标识常量存在（服务端可归因）
#   5. 合规文档齐全（SECURITY.md / COMPLIANCE.md）
# 退出码非 0 = 合规检查失败（run_tests.sh [T7] 与 CI 使用）。
set -u
cd "$(dirname "$0")/.."
FAIL=0
ok()   { echo "  [OK] $*"; }
bad()  { echo "  [FAIL] $*"; FAIL=1; }

MODULE=$(go list -m 2>/dev/null)
echo "== 1. 核心零第三方依赖（B-1/H-4 冻结约束） =="
DEPS=$(go list -m all | grep -v "^$MODULE$")
if [ -z "$DEPS" ]; then
  ok "go list -m all 仅含 $MODULE"
else
  bad "发现第三方依赖:
$DEPS"
fi

echo "== 2. LICENSE =="
if [ ! -f LICENSE ]; then
  bad "LICENSE 缺失"
elif ! grep -qi "MIT License" LICENSE; then
  bad "LICENSE 非 MIT"
elif ! grep -q "Copyright (c) 2026 Mao-jh" LICENSE; then
  bad "LICENSE 持有人与仓库（Mao-jh）不一致"
else
  ok "MIT, Copyright Mao-jh"
fi

echo "== 3. 遥测关键词扫描（核心 + tui + mcp 源码，排除 _test.go） =="
HITS=$(grep -rniE "telemetry|analytics|sentry|mixpanel|posthog" \
  --include="*.go" cli network io persist hash scheduler retry tui mcp 2>/dev/null \
  | grep -v "_test.go" | grep -v "^Binary" || true)
if [ -z "$HITS" ]; then
  ok "无遥测/分析相关代码引用"
else
  bad "疑似遥测引用（须人工确认并移除或注明豁免理由）:
$HITS"
fi

echo "== 4. UA 自标识 =="
if grep -q 'DefaultUserAgent = "Porter/' network/transport.go; then
  ok "network.DefaultUserAgent 存在（Porter/<ver> + 仓库链接）"
else
  bad "DefaultUserAgent 缺失或格式不符"
fi

echo "== 5. 合规文档 =="
for f in SECURITY.md COMPLIANCE.md LICENSE; do
  [ -f "$f" ] && ok "$f" || bad "$f 缺失"
done

if [ $FAIL -eq 0 ]; then echo "== 合规检查全部通过 =="; else echo "== 合规检查失败 =="; fi
exit $FAIL
