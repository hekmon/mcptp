package client

import (
	"context"
	"fmt"

	"github.com/hekmon/mcproxy/server"

	"github.com/urfave/cli/v3"
)

var Command = &cli.Command{
	Name:        "client",
	Usage:       "Act as the proxy client",
	ArgsUsage:   fmt.Sprintf("ws(s)://proxy-server-address:%d", server.DefaultPort),
	Description: "It will connect to the proxy server and forward stdin to it while forwarding back the server's response to stdout. To be launched by your application expecting a stdio MCP server.",
	Flags:       []cli.Flag{},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		return nil
	},
}
