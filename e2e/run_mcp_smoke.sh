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
sleep 1.5
URL=$(grep '^url=' "$ROOT/e2e/mcp_ts.log" | cut -d= -f2- | sed 's|/file/.*||')/file/f.bin
echo "url=$URL"
python "$ROOT/scripts/mcp_smoke.py" "$ROOT/mcp/downloader-mcp.exe" "$URL" "$ROOT/e2e/mcpout"
EC=$?
kill $TS_PID 2>/dev/null || true
exit $EC
