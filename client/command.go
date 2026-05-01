package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hekmon/mcptp/protocol"

	"github.com/coder/websocket"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/urfave/cli/v3"
)

var (
	// Configuration
	target          *url.URL
	tlsConf         *tls.Config
	compressionMode websocket.CompressionMode
)

var Command = &cli.Command{
	Name:        "client",
	Aliases:     []string{"c"},
	Usage:       "Act as the proxy client",
	ArgsUsage:   fmt.Sprintf("ws(s)://proxy-server-address:%d", protocol.DefaultPort),
	Description: "It will connect to the proxy server and forward stdin to it while forwarding back the server's response to stdout. To be launched by your application expecting a stdio MCP server.",
	Flags: []cli.Flag{
		// mTLS
		&cli.StringFlag{
			Name:     "cert",
			Usage:    "Path to the client certificate file",
			Aliases:  []string{"c"},
			Category: "mTLS",
			OnlyOnce: true,
		},
		&cli.StringFlag{
			Name:     "key",
			Usage:    "Path to the client key file",
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
	Before: func(ctx context.Context, cmd *cli.Command) (actionCtx context.Context, err error) {
		defer func() {
			if err != nil {
				err = cli.Exit(err, 1)
			}
		}()
		// Validate the URL
		if cmd.Args().Len() != 1 {
			err = errors.New("expected exactly one argument: the websocket URL")
			return
		}
		if target, err = url.Parse(cmd.Args().Get(0)); err != nil {
			err = fmt.Errorf("invalid URL: %w", err)
			return
		}
		// Handle mTLS
		certFile := cmd.String("cert")
		keyFile := cmd.String("key")
		caFile := cmd.String("ca")
		if certFile != "" || keyFile != "" || caFile != "" {
			if certFile == "" || keyFile == "" || caFile == "" {
				err = errors.New("--cert, --key and --ca must be specified together")
				return
			}
			if tlsConf, err = protocol.GetClientTLSConfig(caFile, certFile, keyFile); err != nil {
				err = fmt.Errorf("failed to generate client TLS configuration: %w", err)
				return
			}
		}
		// Validate configuration based on the URL scheme
		switch target.Scheme {
		case "ws":
			if tlsConf != nil {
				err = errors.New("TLS configuration is not supported for ws:// URLs")
				return
			}
		case "wss":
			if tlsConf == nil {
				err = errors.New("TLS configuration is required for wss:// URLs")
				return
			}
		default:
			err = fmt.Errorf("invalid URL scheme, expecting 'ws' or 'wss': %s", target.Scheme)
			return
		}
		// Adapt compression mode based on the target host
		if isLoopbackHost(target.Host) {
			compressionMode = websocket.CompressionDisabled
		} else {
			compressionMode = protocol.CompressionMode
		}
		// Ready to start
		actionCtx = ctx
		return
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		// Connect to the WebSocket server using a custom HTTP client with our own TLS config (if any)
		httpTransport := cleanhttp.DefaultTransport()
		httpTransport.TLSClientConfig = tlsConf
		conn, _, err := websocket.Dial(ctx, target.String(), &websocket.DialOptions{
			HTTPClient: &http.Client{
				Transport: httpTransport,
			},
			CompressionMode: compressionMode,
		})
		if err != nil {
			return cli.Exit(fmt.Errorf("failed to connect to server: %w", err), 2)
		}
		defer func() {
			// Safety net to ensure the connection is closed if the proxy fails to properly close it
			if err := conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
				fmt.Fprintf(os.Stderr, "warning: failed to close websocket: %v\n", err)
			}
		}()
		// Start proxying
		if err = proxy(ctx, conn); err != nil {
			err = cli.Exit(err, 3)
		}
		return
	},
}

// isLoopbackHost checks if the given host (possibly including port) is a loopback address.
// Handles IPv4 (127.x.x.x), IPv6 (::1), and hostnames (e.g., localhost, myserver.example.com).
// IPv6 addresses in hostport must be enclosed in square brackets (e.g., "[::1]:80").
// The host is always resolved (if not already an IP) and ALL resolved IPs must be loopback addresses.
// This ensures we're absolutely certain before treating a connection as loopback (e.g., to disable compression).
func isLoopbackHost(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		// No port, use the host as-is
		host = hostPort
	}
	// Strip brackets from IPv6 addresses (e.g., "[::1]" -> "::1")
	host = strings.Trim(host, "[]")
	// Parse as IP address and check if loopback
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// Not a valid IP address, resolve it (handles "localhost" and other DNS names)
	ips, err := net.LookupHost(host)
	if err != nil {
		// DNS resolution failed, assume it's not a loopback address
		return false
	}
	// ALL resolved IPs must be loopback addresses
	for _, resolvedIP := range ips {
		if ip := net.ParseIP(resolvedIP); ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}
