package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/hekmon/mcptp/client"
	"github.com/hekmon/mcptp/server"

	"github.com/urfave/cli/v3"
)

func main() {
	// Application-wide signal handling
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,    // Ctrl+C (all platforms)
		syscall.SIGTERM, // systemd stop (Unix)
		syscall.SIGQUIT, // debug dump (Unix)
	)
	defer stop()
	// Start application
	cmd := &cli.Command{
		Name:    "mcptp",
		Usage:   "MCP Teleport: A network proxy for stdio only MCP servers",
		Version: version(),
		Commands: []*cli.Command{
			client.Command,
			server.Command,
		},
	}
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func version() string {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("%s (%s, %s/%s)", infos.Main.Version, infos.GoVersion, runtime.GOOS, runtime.GOARCH)
}
