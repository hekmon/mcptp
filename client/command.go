package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/hekmon/mcptp/server"

	"github.com/urfave/cli/v3"
)

var (
	// Runtime
	target *url.URL
)

var Command = &cli.Command{
	Name:        "client",
	Usage:       "Act as the proxy client",
	ArgsUsage:   fmt.Sprintf("ws(s)://proxy-server-address:%d", server.DefaultPort),
	Description: "It will connect to the proxy server and forward stdin to it while forwarding back the server's response to stdout. To be launched by your application expecting a stdio MCP server.",
	Flags:       []cli.Flag{},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		// Validate the URL
		if cmd.Args().Len() != 1 {
			return ctx, fmt.Errorf("expected exactly one argument: the websocket URL")
		}
		var err error
		if target, err = url.Parse(cmd.Args().Get(0)); err != nil {
			return ctx, fmt.Errorf("invalid URL: %w", err)
		}
		switch target.Scheme {
		case "ws":
		case "wss":
			return ctx, errors.New("TLS not yet implemented")
		default:
			return ctx, fmt.Errorf("invalid URL scheme, expecting 'ws' or 'wss': %s", target.Scheme)
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		return nil
	},
}
