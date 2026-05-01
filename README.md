# MCP Teleport (mcptp)

**MCP Teleport** is a secure network proxy that lets you run Model Context Protocol (MCP) servers remotely while maintaining the experience of running them locally. Your MCP client connects to a local proxy, which securely forwards all communication to the actual MCP server running on a remote machine.

## Why Use MCP Teleport?

- **Remote Execution**: Run resource-intensive MCP servers on powerful remote machines while developing locally
- **Cross-Platform Development**: Use Linux-only MCP servers from Windows via WSL ([see use case](#use-case-windows-ide-with-linux-only-mcp-server))
- **Transparent Operation**: Your MCP client thinks it's talking to a local stdio server—no configuration changes needed
- **Security Options**: Mutual TLS (mTLS) encryption available for untrusted networks, plain mode for trusted networks
- **Simple Design**: One connection equals one spawned process, mirroring standard local execution behavior

## How It Works

```
┌─────────────────┐     ┌──────────────┐     ┌─────────────────┐     ┌──────────────────┐
│   MCP Client    │────>│ Client Proxy │────>│ Server Proxy    │────>│ MCP Server       │
│   (e.g., IDE)   │<────│ (on your PC) │<────│ (on remote host)│<────│ (actual service) │
└─────────────────┘     └──────────────┘     └─────────────────┘     └──────────────────┘
      stdio        spawns              WebSocket                spawns      stdio
```

1. **Local Proxy**: Launched by your MCP client instead of the real server. It appears as a standard stdio MCP server to your client.
2. **Remote Proxy**: Runs on the remote machine, accepts secure connections, and spawns the actual MCP server process.
3. **Transparent Forwarding**: All MCP messages flow bidirectionally between client and server through the proxies.

## Quick Start

### Installation

#### Option 1: Precompiled Binaries (Recommended)

Download the latest precompiled binary for your platform from the [GitHub Releases page](https://github.com/hekmon/mcptp/releases).

#### Option 2: Build from Source

If you have Go installed, you can build from source:

```bash
go install github.com/hekmon/mcptp@latest
```

This will place the binary in your `GOPATH/bin` directory.

### Basic Setup (Trusted Network / VPN)

If you're running over a VPN or trusted network, you can use plain WebSocket without TLS:

**On the Remote Machine:**
```bash
mcptp server --bind 0.0.0.0 --port 7777 -- /path/to/your/mcp-server [args...]
```

**On Your Local Machine:**
```bash
mcptp client ws://remote-host:7777
```

Then configure your MCP client to launch `mcptp client` instead of the MCP server directly.

### Secure Setup (Public Networks)

For connections over untrusted networks, use mutual TLS (mTLS) encryption and authentication:

**1. Generate Certificates (once):**
```bash
mcptp mTLS --output ./certs
```

This creates:
- `ca.crt` - Certificate Authority (trust bundle)
- `server.crt` + `server.key` - Server certificate and key
- `client.crt` + `client.key` - Client certificate and key

**Important Security Note**: The CA private key is **never written to disk**. It exists only in memory during generation and is discarded immediately. This means:
- ✅ Only certificates generated together can communicate (closed trust bundle)
- ✅ No additional certificates can be created later
- ✅ No risk of CA key theft from disk
- ℹ️ To rotate certificates, regenerate the entire bundle

**2. Deploy to Remote Machine:**
Copy `ca.crt`, `server.crt`, and `server.key` to your remote machine.

**3. Start Remote Proxy:**
```bash
mcptp server --bind 0.0.0.0 --port 7777 \
  --cert /path/to/server.crt \
  --key /path/to/server.key \
  --ca /path/to/ca.crt \
  -- /path/to/your/mcp-server [args...]
```

**4. Configure Local Proxy:**
```bash
mcptp client wss://remote-host:7777 \
  --cert /path/to/client.crt \
  --key /path/to/client.key \
  --ca /path/to/ca.crt
```

## Security & TLS

### When to Use TLS (wss://)

| Scenario | Recommendation |
|----------|----------------|
| Localhost / same machine | Plain `ws://` (TLS optional) |
| Trusted VPN / isolated network | Plain `ws://` (TLS optional) |
| Public internet / untrusted network | **mTLS required** (`wss://`) |

### How mTLS Works

MCP Teleport uses mutual TLS authentication where **both parties verify each other**:

1. **Client verifies Server**: Ensures it's connecting to a legitimate server (not an imposter)
2. **Server verifies Client**: Ensures only authorized clients can connect

Both certificates are signed by the same CA and include special markers (EKU—Extended Key Usage) that prevent a client certificate from being misused as a server certificate, and vice versa.

**No hostname verification**: Unlike typical HTTPS, mcptp does not verify hostnames or IP addresses in certificates. Trust is established purely through:
- Certificate signed by the trusted CA
- Correct role marker (serverAuth or clientAuth)

This simplifies deployment for personal servers where you control both ends.

### Certificate Properties

- **Algorithm**: ECDSA P-256 (modern, efficient, widely supported)
- **Validity**: 10 years (rotation is manual)
- **Key Size**: 256-bit ECDSA
- **TLS Version**: TLS 1.3 only

## Features

### Automatic Compression

WebSocket compression is automatically managed based on the connection type:

- **Localhost connections** (`127.0.0.1`, `::1`, `localhost`): Compression is **disabled** (no overhead needed for local traffic)
- **Remote connections**: Compression is **enabled** (reduces bandwidth usage over network)

You don't need to configure this manually—the client automatically detects loopback addresses and optimizes accordingly.

## Configuration

```
$ ./mcptp -h
NAME:
   mcptp - MCP Teleport: A network proxy for stdio only MCP servers

USAGE:
   mcptp [global options] [command [command options]]

VERSION:
   v1.0.0 (go1.26.0, linux/amd64)

COMMANDS:
   client, c  Act as the proxy client
   server, s  Act as the proxy server
   mTLS, t    Create certificates needed for mTLS (mutual TLS)
   help, h    Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --help, -h     show help
   --version, -v  print the version
```

### Client Command

```
$ ./mcptp client -h
NAME:
   mcptp client - Act as the proxy client

USAGE:
   mcptp client [options] ws(s)://proxy-server-address:8623

DESCRIPTION:
   It will connect to the proxy server and forward stdin to it while forwarding back the server's response to stdout. To be launched by your application expecting a stdio MCP server.

OPTIONS:
   --help, -h  show help

   mTLS

   --ca string, -a string    Path to the CA certificate file
   --cert string, -c string  Path to the TLS certificate file
   --key string, -k string   Path to the TLS key file
```

### Server Command

```
$ ./mcptp server -h
NAME:
   mcptp server - Act as the proxy server

USAGE:
   mcptp server [options] -- path/to/mcpserver [mcpserver args...]

DESCRIPTION:
   It will start as the proxy server and spawn the process at each new connection forwarding the stdin/stdout to the client.

OPTIONS:
   --help, -h                     show help
   --log-level string, -l string  Set the logging level. Valid values: DEBUG, INFO, WARN, ERROR (default: "INFO")

   HTTP server

   --bind string, -b string       Address to bind the server to. Prefer local addresses if no mTLS. Use 0.0.0.0 to bind to all v4 interfaces and :: to bind to all v6 interfaces.
   --max-connections int, -n int  Maximum number of connections to accept. Each connection spawns a new process. 0 means illimited. (default: 0)
   --port int, -p int             Port to use for the server. (default: 8623)

   mTLS

   --ca string, -a string    Path to the CA certificate file
   --cert string, -c string  Path to the TLS certificate file
   --key string, -k string   Path to the TLS key file
```

### Certificate Generation

```
$ ./mcptp mTLS -h
NAME:
   mcptp mTLS - Create certificates needed for mTLS (mutual TLS)

USAGE:
   mcptp mTLS [options]

DESCRIPTION:
   Generates a complete mTLS certificate bundle for secure client-server authentication (wss://) over untrusted networks. This command creates a self-contained PKI with: a CA certificate (ca.crt), server certificate and key (server.crt, server.key) with serverAuth EKU, and client certificate and key (client.crt, client.key) with clientAuth EKU. The CA private key is NOT persisted, ensuring no additional certificates can be issued later. To rotate certificates, regenerate the entire bundle. For trusted networks or VPNs, plain WebSocket (ws://) without TLS is acceptable.

OPTIONS:
   --output string, -o string  Output directory for certificates (default: current directory) (default: ".")
   --help, -h                  show help
```

## Example: Running a Time/Date MCP Server Remotely

**Remote Machine:**
```bash
# Generate certificates
mcptp mTLS --output ~/mcptp-certs

# Start server (copy certs to remote first)
mcptp server --bind 0.0.0.0 --port 7777 \
  --cert ~/mcptp-certs/server.crt \
  --key ~/mcptp-certs/server.key \
  --ca ~/mcptp-certs/ca.crt \
  -- uvx mcp-server-time
```

**Local Machine:**
```bash
# Connect (copy client certs from remote)
mcptp client wss://my-server.example.com:7777 \
  --cert ~/mcptp-certs/client.crt \
  --key ~/mcptp-certs/client.key \
  --ca ~/mcptp-certs/ca.crt
```

**MCP Client Configuration:**
```json
{
  "mcpServers": {
    "time": {
      "command": "mcptp",
      "args": ["client", "--cert", "...", "--key", "...", "--ca", "...", "wss://my-server.example.com:7777"]
    }
  }
}
```

## Use Case: Windows IDE with Linux-Only MCP Server

If you develop on Windows but need to use an MCP server that only runs on Linux (e.g., it depends on Linux-specific tools or libraries), mcptp can bridge the gap using WSL (Windows Subsystem for Linux):

**Architecture:**
```
┌──────────────────────────────────────────────────┐
│  Windows Host                                    │
│  ┌─────────────┐              ┌──────────────┐   │
│  │  IDE (VS    │─────────────>│ mcptp client │   │
│  │  Code, etc) │<─────────────│  (Windows)   │   │
│  └─────────────┘              └──────┬───────┘   │
│                                      │ localhost │
└──────────────────────────────────────┼───────────┘
                   ws://localhost:7777 │
┌──────────────────────────────────────┼───────────┐
│  WSL (Windows Subsystem for Linux)   | localhost │
│  ┌──────────────────┐       ┌────────┴────────┐  │
│  │ MCP Server       │──────>│  mcptp server   │  │
│  │ (Linux-only)     │<──────│  (Linux binary) │  │
│  └──────────────────┘       └─────────────────┘  │
└──────────────────────────────────────────────────┘
```

**Setup Steps:**

**1. Find your WSL IP address** (in Windows PowerShell):
```powershell
# This runs 'hostname -I' inside WSL and returns the IP
wsl hostname -I
```
Note the IP address (e.g., `172.25.123.45` in NAT mode, or `127.0.0.1` in mirrored mode).

**2. In WSL (Linux) - Start the server:**
```bash
# Bind to the WSL IP address found in step 1, or 127.0.0.1 if using mirrored mode
mcptp server --bind <WSL-IP> --port 7777 -- /path/to/linux-only-mcp-server
```

**3. In Windows - Configure your IDE:**
```json
{
  "mcpServers": {
    "your-linux-mcp-server": {
      "command": "mcptp",
      "args": ["client", "ws://<WSL-IP>:7777"]
    }
  }
}
```

> **Tip:** Use the same IP address for both `--bind` (in WSL) and the client URL (in Windows).
> **Tip:** Enable WSL mirrored networking mode to consistently use `127.0.0.1`. This avoids having to look up the IP address on each restart. To enable mirrored networking, add this to your `%USERPROFILE%\.wslconfig`:
> ```ini
> [wsl2]
> networkingMode=mirrored
> ```
> Then restart WSL with `wsl --shutdown`.

That's it! Since all traffic stays on your local machine (Windows ↔ WSL), there's no need for certificate generation or TLS encryption. This setup lets your Windows-based IDE seamlessly use Linux-exclusive MCP servers as if they were native Windows processes.

## Troubleshooting

### Connection Refused
- Ensure the remote proxy is running and listening on the correct address
- Check firewall rules on the remote machine
- For `wss://`, verify port 7777 (or your chosen port) is open

### TLS Handshake Failed
- Verify all three certificate files (`ca.crt`, `client.crt`, `client.key`) are present on the client
- Ensure certificates were generated together as a bundle
- Check that the server has `server.crt`, `server.key`, and `ca.crt`

### Certificate Expired
- Certificates are valid for 10 years from generation
- If expired, regenerate the entire bundle with `mcptp mTLS` and redistribute

### Server Crashes on Connect
- Check remote proxy logs for the actual MCP server error
- Verify the MCP server path and arguments are correct
- Some MCP servers don't support concurrent access—use `--max-connections=1`

## Architecture Notes

This README covers user-facing functionality. For complete technical specifications including:
- WebSocket message framing
- Process lifecycle management
- Concurrency model
- Stream behavior
- Detailed protocol specification

See [AGENTS.md](AGENTS.md).

## License

[See LICENSE file](LICENSE)

## Support

This is a personal/self-hosted tool optimized for individual developers and small teams. For issues or questions, please file an issue on the GitHub repository.
