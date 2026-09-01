#!/bin/bash
# MCP stdio 冒烟：模拟 AI 客户端 initialize → tools/list → download_start → 轮询 status
set -e
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)
DATA=mcpdata
rm -rf "$DATA" mcpout mcp_ts.log
mkdir -p "$DATA" mcpout

(cd "$ROOT/bin" && ./testserver.exe -dir "$ROOT/e2e/$DATA" -name f.bin -size $((8*1024*1024)) > "$ROOT/e2e/mcp_ts.log" 2>&1) &
TS_PID=$!
# 轮询等待 url 就绪（Windows 下进程首次启动可能超过 1s，固定 sleep 会偶发失败）
BASE=""
for _ in $(seq 1 20); do
  BASE=$(grep '^url=' "$ROOT/e2e/mcp_ts.log" 2>/dev/null | cut -d= -f2- | sed 's|/file/.*||')
  [ -n "$BASE" ] && break
  sleep 0.5
done
[ -n "$BASE" ] || { echo "未取得 testserver url"; kill $TS_PID 2>/dev/null || true; exit 1; }
URL="$BASE/file/f.bin"
echo "url=$URL"
python "$ROOT/scripts/mcp_smoke.py" "$ROOT/mcp/porter-mcp.exe" "$URL" "$ROOT/e2e/mcpout"
EC=$?
kill $TS_PID 2>/dev/null || true
exit $EC
