package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/hekmon/mcptp/protocol"

	"github.com/urfave/cli/v3"
)

var (
	// Config
	bindAddress      string
	port             int
	maxConnections   int64
	logLevel         string
	mcpServerCmdline []string
	tlsConf          *tls.Config
	// Runtime
	logger *slog.Logger
)

var Command = &cli.Command{
	Name:        "server",
	Aliases:     []string{"s"},
	Usage:       "Act as the proxy server",
	Description: "It will start as the proxy server and spawn the process at each new connection forwarding the stdin/stdout to the client.",
	ArgsUsage:   "-- path/to/mcpserver [mcpserver args...]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:             "log-level",
			Usage:            fmt.Sprintf("Set the logging level. Valid values: %s", strings.Join(GetLogLevels(), ", ")),
			Aliases:          []string{"l"},
			OnlyOnce:         true,
			Value:            DefaultLogLevel.String(),
			Destination:      &logLevel,
			ValidateDefaults: true,
			Validator: func(value string) error {
				if !slices.Contains(GetLogLevels(), strings.ToUpper(value)) {
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
			Value:            protocol.DefaultPort,
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
		// mTLS
		&cli.StringFlag{
			Name:     "cert",
			Usage:    "Path to the server certificate file",
			Aliases:  []string{"c"},
			Category: "mTLS",
			OnlyOnce: true,
		},
		&cli.StringFlag{
			Name:     "key",
			Usage:    "Path to the server key file",
			Aliases:  []string{"k"},
			Category: "mTLS",
			OnlyOnce: true,
		},
		&cli.StringFlag{
			Name:     "ca",
			Usage:    "Path to the CA certificate file",
			Aliases:  []string{"a"},
			Category: "mTLS",
			OnlyOnce: true,
		},
	},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		// Verify the command to spawn
		if cmd.Args().Len() == 0 {
			return ctx, cli.Exit(errors.New("no command to spawn on incoming connections"), 1)
		}
		mcpServerCmdline = cmd.Args().Slice()
		var err error
		if mcpServerCmdline[0], err = exec.LookPath(mcpServerCmdline[0]); err != nil {
			return ctx, cli.Exit(fmt.Errorf("command %q not found: %w", mcpServerCmdline[0], err), 1)
		}
		// Handle mTLS
		certFile := cmd.String("cert")
		keyFile := cmd.String("key")
		caFile := cmd.String("ca")
		if certFile != "" || keyFile != "" || caFile != "" {
			if certFile == "" || keyFile == "" || caFile == "" {
				return ctx, cli.Exit(errors.New("--cert, --key and --ca must be specified together"), 1)
			}
			if tlsConf, err = protocol.GetServerTLSConfig(caFile, certFile, keyFile); err != nil {
				return ctx, cli.Exit(fmt.Errorf("failed to generate server TLS configuration: %w", err), 1)
			}
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		// Prepare
		logger = CreateLogger(logLevel)
		var scheme string
		if tlsConf == nil {
			scheme = "ws"
		} else {
			scheme = "wss"
		}
		// Create the HTTP server
		logger.Info("starting proxy server",
			slog.String("listen", fmt.Sprintf("%s://%s:%d", scheme, bindAddress, port)),
			slog.Int64("max-connections", maxConnections),
			slog.String("command", strings.Join(mcpServerCmdline, " ")),
		)
		var requests sync.WaitGroup
		httpServer := &http.Server{
			Addr:    fmt.Sprintf("%s:%d", bindAddress, port),
			Handler: http.HandlerFunc(handleConnection(ctx, &requests)),
		}
		// Prepare for clean shutdown
		go cleanShutdown(ctx, httpServer)
		// Start the HTTP server
		if tlsConf != nil {
			// We manually created the TLS config and parsed certificates, we won't be using the high level ListenAndServeTLS()
			ln, err := net.Listen("tcp", httpServer.Addr)
			if err != nil {
				return err
			}
			if err = httpServer.Serve(tls.NewListener(ln, tlsConf)); err != nil && err != http.ErrServerClosed {
				return cli.Exit(fmt.Errorf("failed to run HTTPS server: %w", err), 2)
			}
		} else {
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return cli.Exit(fmt.Errorf("failed to run HTTP server: %w", err), 2)
			}
		}
		// Shutdown has been called, but we must wait for all in flight requests to complete
		logger.Info("waiting for in-flight websocket requests to end")
		requests.Wait()
		return nil
	},
}

func cleanShutdown(ctx context.Context, httpServer *http.Server) {
	// wait for signal
	<-ctx.Done()
	logger.Info("shutdown signal received",
		slog.Int64("active_connections", nbConn.Load()),
	)
	// Use context.Background() without timeout: all connections are hijacked
	// WebSockets, which are no longer tracked by the HTTP server. Shutdown()
	// only stops accepting new connections and returns instantly for hijacked
	// connections. The actual connection cleanup is handled by processCtx
	// cancellation and requests.Wait() in the Command Action function.
	if err := httpServer.Shutdown(context.Background()); err != nil {
		logger.Error("failed to shutdown HTTP server", slog.Any("error", err))
	} else {
		logger.Info("HTTP server shutdown complete (not accepting new connections)")
	}
}
