#!/bin/bash
# rename_module.sh — 一键更换 Go module 路径（发布到 GitHub 时与仓库名对齐）
# 用法：./scripts/rename_module.sh github.com/<user>/<repo>
set -e
cd "$(dirname "$0")/.."
NEW="$1"
[ -n "$NEW" ] || { echo "用法: $0 github.com/<user>/<repo>"; exit 2; }
OLD_ROOT="module downloader"
OLD_IMPORT='"downloader/'

echo "重命名模块路径: downloader -> $NEW"

# 1) 根模块声明
sed -i "s|^${OLD_ROOT}$|module ${NEW}|" go.mod

# 2) 根模块内全部 import（含测试）
grep -rl "${OLD_IMPORT}" --include="*.go" cmd cli scheduler network io persist hash retry testserver e2e 2>/dev/null |
  while read -r f; do sed -i "s|${OLD_IMPORT}|\"${NEW}/|g" "$f"; done

# 3) 子模块（tui/gui）：require + replace + import
for sub in tui gui; do
  [ -d "$sub" ] || continue
  sed -i "s|^module downloader/${sub}$|module ${NEW}/${sub}|" "$sub/go.mod"
  sed -i "s|^require downloader v0.0.0$|require ${NEW} v0.0.0|" "$sub/go.mod"
  sed -i "s|^replace downloader => ../$|replace ${NEW} => ../|" "$sub/go.mod"
  grep -rl "${OLD_IMPORT}" --include="*.go" "$sub" 2>/dev/null |
    while read -r f; do sed -i "s|${OLD_IMPORT}|\"${NEW}/|g" "$f"; done
done

echo "完成。验证："
gofmt -l . | head -3
(cd . && go build ./... && go vet ./... && echo "root OK")
(cd tui && go build ./... && echo "tui OK")
echo "如需二次改名：再次运行本脚本并传新路径即可。"
