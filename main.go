package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:      "mcproxy",
		Usage:     "A network proxy for stdio only MCP servers",
		Version:   version(),
		Commands:  []*cli.Command{},
	}
	err := cmd.Run(context.Background(), os.Args)
	if err != nil {
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
