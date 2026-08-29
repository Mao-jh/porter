// Command downloader-mcp 即 Porter —— 下载器的 MCP 服务器（stdio 传输），
// 供 AI 客户端（ZCode / Claude Desktop / Cursor 等）作为插件接入。
//
// 客户端配置示例（mcp.json）：
//
//	{
//	  "mcpServers": {
//	    "downloader": {
//	      "command": "downloader-mcp",
//	      "args": ["-state-root", ".downloader-mcp"]
//	    }
//	  }
//	}
//
// 工具：download_start / download_status / download_cancel / list_tasks。
// 安全：默认仅允许 127.0.0.0/8 回环目标（H-3）；-allow-remote 为显式产品开关。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	mcpserver "github.com/Mao-jh/downloader/mcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	var (
		stateRoot   = flag.String("state-root", ".downloader-mcp", "状态根目录（每任务一个子目录）")
		allowRemote = flag.Bool("allow-remote", false, "允许非回环目标（默认仅 127.0.0.0/8；显式产品开关）")
		limit       = flag.Int64("limit", 0, "全局下载限速 字节/秒（0=不限）")
		verify      = flag.String("verify", "sha256", "校验算法: sha256|sha1|md5|none")
		outDir      = flag.String("out", "", "默认输出目录（空=进程工作目录）")
	)
	flag.Parse()

	cfg := mcpserver.Config{
		StateRoot:   *stateRoot,
		AllowRemote: *allowRemote,
		Verify:      *verify,
		Limit:       *limit,
		OutputDir:   *outDir,
	}
	server := mcpserver.NewToolServer(cfg)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "downloader-mcp:", err)
		os.Exit(1)
	}
}
