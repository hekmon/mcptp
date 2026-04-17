package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/hekmon/mcproxy/logging"

	"github.com/urfave/cli/v3"
)

const (
	DefaultPort = 8623
)

var (
	// Config
	bindAddress    string
	port           int
	maxConnections int64
	logLevel       string
	// Runtime
	nbConn           atomic.Int64
	mcpServerCmdline []string
	logger           *slog.Logger
)

var Command = &cli.Command{
	Name:        "server",
	Usage:       "Act as the proxy server",
	Description: "It will start as the proxy server and spawn the process at each new connection forwarding the stdin/stdout to the client.",
	ArgsUsage:   "-- path/to/mcpserver [mcpserver args...]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:             "log-level",
			Usage:            fmt.Sprintf("Set the logging level. Valid values: %s", strings.Join(logging.GetLogLevels(), ", ")),
			Aliases:          []string{"l"},
			OnlyOnce:         true,
			Value:            logging.DefaultLevel.String(),
			Destination:      &logLevel,
			ValidateDefaults: true,
			Validator: func(value string) error {
				if !slices.Contains(logging.GetLogLevels(), strings.ToUpper(value)) {
					return fmt.Errorf("invalid log level: %q", value)
				}
				return nil
			},
		},
		// HTTP server
		&cli.StringFlag{
			Name:        "bind",
			Usage:       "Address to bind the server to. Prefer local addresses if no mTLS. Use 0.0.0.0 to bind to all v4 interfaces and :: to bind to all v6 interfaces.",
			Aliases:     []string{"b"},
			Category:    "HTTP server",
			OnlyOnce:    true,
			Required:    true,
			Destination: &bindAddress,
		},
		&cli.IntFlag{
			Name:             "port",
			Usage:            "Port to use for the server.",
			Aliases:          []string{"p"},
			Category:         "HTTP server",
			OnlyOnce:         true,
			Destination:      &port,
			Value:            DefaultPort,
			ValidateDefaults: true,
			Validator: func(value int) error {
				if value < 1 {
					return errors.New("must be >= 1")
				}
				if value > 65535 {
					return errors.New("must be <= 65535")
				}
				return nil
			},
		},
		&cli.Int64Flag{
			Name:        "max-connections",
			Usage:       "Maximum number of connections to accept. Each connection spawns a new process. 0 means illimited.",
			Aliases:     []string{"n"},
			Category:    "HTTP server",
			OnlyOnce:    true,
			Destination: &maxConnections,
			Validator: func(value int64) error {
				if value < 0 {
					return errors.New("must be => 0")
				}
				return nil
			},
		},
	},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		// Verify the command to spawn
		if cmd.Args().Len() == 0 {
			return ctx, errors.New("no command to spawn on incoming connections")
		}
		var err error
		mcpServerCmdline = cmd.Args().Slice()
		if mcpServerCmdline[0], err = exec.LookPath(mcpServerCmdline[0]); err != nil {
			return ctx, fmt.Errorf("command %q not found: %w", mcpServerCmdline[0], err)
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		// Create the logger
		logger = logging.CreateLogger(logLevel)
		logger.Info("starting proxy server",
			slog.String("listen", fmt.Sprintf("ws://%s:%d", bindAddress, port)),
			slog.Int64("max-connections", maxConnections),
			slog.String("command", strings.Join(mcpServerCmdline, " ")),
		)
		// Create the HTTP server
		httpServer := &http.Server{
			Addr:    fmt.Sprintf("%s:%d", bindAddress, port),
			Handler: http.HandlerFunc(incomingConnection),
		}
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return cli.Exit(fmt.Errorf("failed to run HTTP server: %w", err), 1)
		}
		return nil
	},
}
