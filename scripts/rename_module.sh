#!/bin/bash
# rename_module.sh — 一键更换 Go module 路径（与 GitHub 仓库名对齐）
# 用法：./scripts/rename_module.sh github.com/<user>/<repo>
# 幂等：自动从根 go.mod 读取当前路径，任意次数重跑皆可。
set -e
cd "$(dirname "$0")/.."
NEW="$1"
[ -n "$NEW" ] || { echo "用法: $0 github.com/<user>/<repo>"; exit 2; }

OLD=$(head -1 go.mod | awk '{print $2}')
[ "$OLD" = "module" ] && OLD=$(sed -n '1s/^module //p' go.mod)
[ -n "$OLD" ] || { echo "无法从 go.mod 读取当前模块路径"; exit 2; }
[ "$OLD" = "$NEW" ] && { echo "模块路径已是 $NEW，无需改名"; exit 0; }

echo "模块路径: $OLD -> $NEW"

# 根模块声明与全部 import
sed -i "s|^module ${OLD}$|module ${NEW}|" go.mod
grep -rl "\"${OLD}/" --include="*.go" . 2>/dev/null |
  while read -r f; do sed -i "s|\"${OLD}/|\"${NEW}/|g" "$f"; done

# 子模块：module 声明 / require / replace / import
for sub in tui gui mcp; do
  [ -f "$sub/go.mod" ] || continue
  sed -i "s|^module ${OLD}/${sub}$|module ${NEW}/${sub}|" "$sub/go.mod"
  sed -i "s|^\t${OLD} v0.0.0|\t${NEW} v0.0.0|" "$sub/go.mod"
  sed -i "s|^replace ${OLD} => ../$|replace ${NEW} => ../|" "$sub/go.mod"
  grep -rl "\"${OLD}/" --include="*.go" "$sub" 2>/dev/null |
    while read -r f; do sed -i "s|\"${OLD}/|\"${NEW}/|g" "$f"; done
done

echo "完成。构建验证："
gofmt -l . | head -3
go build ./... && go vet ./... && echo "root OK"
( cd tui && GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1 || true; go build ./... && echo "tui OK" )
( cd mcp && GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1 || true; go build ./... && echo "mcp OK" )
echo "如需再次改名：重新运行本脚本传新路径即可。"
