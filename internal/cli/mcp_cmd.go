package cli

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/mcpserver"
)

func init() {
	register("mcp", "Run the MCP server on stdio (tools: status, task, gate, plan, hands, review)", cmdMCP)
}

func cmdMCP(c *ctx, args []string) error {
	dir := c.dir
	// Claude Code may start MCP servers outside the project; it tells us where the project is.
	if p := os.Getenv("CLAUDE_PROJECT_DIR"); p != "" {
		dir = p
	}
	s := mcpserver.New(func() (*core.Workspace, error) { return core.Open(dir) }, Version)
	return s.Run(context.Background(), &mcp.StdioTransport{})
}
