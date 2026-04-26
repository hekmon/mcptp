# MCP Remote Proxy Specification

Version: 1.0  
Status: Draft  
Scope: Personal/self-hosted developer use

## 1. Goal

This project provides a remote execution bridge for an MCP-compatible server while preserving the local developer experience of a normal stdio-launched server.
From the client point of view, the local proxy behaves like a regular child process exposing MCP over stdin/stdout, while the actual MCP server runs on a remote machine.

The transport layer supports both secure (TLS) and plain connections. TLS is recommended for connections over public networks, but can be disabled when running over a VPN or other trusted secure tunnel.

The system is intentionally **not** a multi-tenant commercial product.  
It is optimized for:
- one developer or a small number of developers,
- personal servers,
- simple certificate generation and rotation,
- minimal PKI complexity,
- maximum behavioral fidelity with a local `exec`.

## 2. Non-goals

This design does **not** attempt to:
- multiplex multiple client sessions into a single MCP server process,
- provide session persistence across reconnects,
- provide certificate revocation infrastructure,
- provide enterprise-grade CA management,
- standardize a new MCP extension.

MCP over stdio is a single process connected by stdin/stdout, and MCP connection lifecycle is stateful rather than stateless, so one shared server process cannot safely serve multiple independent client sessions.

## 3. High-level architecture

The system contains two binaries:

- **Local proxy**: launched by the MCP client instead of the server.
- **Remote proxy**: running on the server, accepting secure WebSocket connections and spawning the real MCP server.

Flow:

```text
MCP Client
 └─ spawns local proxy
     ├─ stdin/stdout/stderr visible to client
     └─ secure WebSocket connection
          └─ remote proxy
               └─ spawns real MCP server
```

The local proxy is the MCP stdio facade.
The remote proxy is a process launcher and transport bridge.  
The actual MCP server remains unaware that it is remote.

## 4. Process model

### 4.1 One connection = one spawn

The core rule is:

- **1 WebSocket connection = 1 spawned MCP server process**

There is no pooling and no reuse of an existing process.  
This mirrors the local `fork/exec + pipes` model as closely as possible.  
It also matches MCP initialization semantics, where a new connection performs its own `initialize` exchange and enters its own active lifecycle.

### 4.2 Lifetime symmetry

The lifetime of the MCP server process is tied to the lifetime of the WebSocket session.

- connection opens -> server is spawned
- connection closes -> server stdin is closed
- server exits -> connection is closed

This keeps the mental model simple and preserves local-exec behavior.

### 4.3 Shutdown model

MCP does not define a dedicated JSON-RPC shutdown method for terminating the process.
Normal termination is driven by transport closure: the client side closes its stream, the server receives EOF on stdin, and exits cleanly if implemented correctly.

Recommended remote shutdown order:
1. close server stdin,
2. wait briefly for clean exit,
3. send SIGTERM if still running,
4. send SIGKILL only as a last resort.

### 4.4 Worker concurrency model

The remote proxy uses **three** independent worker goroutines per connection:

- **receiver**: reads from websocket → writes to process stdin (fatal: exit triggers shutdown)
  - Also accumulates and logs complete JSON-RPC requests from the binary stream
  - Logs: method name, params length, request ID (if present)
- **sender_stdout**: reads from process stdout → writes to websocket binary (fatal: exit triggers shutdown)
  - Also accumulates and logs complete JSON-RPC responses from the binary stream
  - Logs: response ID, is_error flag
- **sender_stderr**: reads from process stderr → sends OoB text messages (**non-fatal**: exit does NOT trigger shutdown)

**Synchronization pattern:**
- Each worker has a `done` channel (buffered, size 1) closed after it finishes
- Error variables are written *before* their done channel is closed
- Reading errors after `<-doneChan` is race-free (channel close provides memory barrier)

**Why not errgroup.WithContext()?**
- `errgroup.WithContext()` cancels the context immediately on first error
- This would prematurely stop other workers mid-operation
- Example: if sender_stdout exits (process died), receiver must still be allowed to return cleanly
- Using individual contexts + channels lets workers finish naturally

**Error semantics:**
- **stdin/stdout workers**: First to exit = **root cause** (acted upon, triggers connection shutdown)
- **stderr worker**: Exit is **ignored for shutdown decisions** (stderr is optional diagnostics)
- `receiver` returns `*websocket.CloseError`:
  - Non-nil: caller should close websocket with these codes (instructions for how TO close)
  - Nil: websocket already dead, no Close() needed
- `sender_stderr` always returns `nil` (never triggers shutdown)

**Shutdown flow:**
1. Client sends OoB shutdown OR websocket closes OR stdout EOF (process exited)
2. receiver OR sender_stdout exits first (whichever detects the event)
3. First exit triggers: cancel receiver context, drain all workers
4. stderr worker drained (may still be running, exits when process closes stderr)
5. Process exits gracefully (or WaitDelay kills it)
6. All workers finished, connection cleanup complete

**stderr worker lifecycle:**
- Only exits on **EOF** (process closed stderr) or **context cancellation** (after main shutdown)
- Read errors, send errors, long lines (>1MB) → logged but **continue reading**
- Never triggers connection shutdown (stderr is optional, non-fatal)

## 5. Transport choice

### 5.1 WebSocket

The transport between local proxy and remote proxy is **WebSocket**.  
WebSocket was chosen because it provides:
- message framing,
- binary and text message types,
- built-in ping/pong,
- easy TLS integration,
- easy client auth integration,
- simpler observability than a custom TCP framing protocol.

### 5.2 TLS modes

Two modes are supported:

- **Secure mode** (`wss://`): WebSocket over TLS with optional mTLS authentication. Recommended for connections over public or untrusted networks.
- **Plain mode** (`ws://`): Unencrypted WebSocket. Suitable only for trusted environments such as VPNs, localhost, or isolated networks.

### 5.3 Address and port separation

The server uses separate `--bind` and `--port` flags rather than a combined `--listen` flag. This provides:
- clearer separation of concerns,
- flexibility to specify address only, port only, or both,
- consistency with common CLI conventions.

Examples:
- `--bind :7777` - bind to all interfaces on port 7777
- `--bind 0.0.0.0 --port 7777` - explicitly bind to IPv4 all interfaces
- `--bind :: --port 7777` - bind to all IPv6 interfaces

### 5.4 Why not raw TCP framing

A custom `[stream][length][payload]` framing scheme would work, but WebSocket removes the need to define and maintain a private framing layer.  
For a small self-hosted proxy, standard transport semantics are preferable to inventing a custom one.



## 6. WebSocket message model

### 6.1 Binary channel

Binary WebSocket messages carry MCP stdout data as a transparent stream.

**Framing rule:**

- **1 `Read()` from MCP server stdout = 1 binary WebSocket message**
- Message boundaries do NOT align with MCP line boundaries (`\n`)

MCP stdio messages are JSON-RPC messages sent over stdin/stdout, one message per line, UTF-8 encoded, and stdout must contain only valid protocol messages.
The proxy forwards each `Read()` result immediately as a WebSocket message without modification.

**Transparent forwarding:**

- Binary data is forwarded byte-for-byte without inspection
- No latency is added to the MCP protocol stream
- The proxy does not block on message parsing

**Observation layer:**

While forwarding is transparent, the proxy also maintains internal line buffers for **observability only**:
- Complete JSON-RPC messages are accumulated from the binary stream
- Each complete message is logged with structured fields (method, ID, etc.)
- Extra complete lines are discarded to maintain protocol synchronization
- This observation is read-only and does not interfere with forwarding

**Parsing scope:**

The proxy's JSON-RPC understanding is intentionally minimal:
- Only extracts metadata: method name, request/response ID, error flag
- Does NOT validate full message semantics per the JSON-RPC specification
- Does NOT validate parameter types, return types, or method-specific semantics
- Does NOT reject or modify messages based on validation failures
- Invalid messages are logged as-is and forwarded unchanged

**Rationale:**

- The proxy extends the kernel pipe over the network (forwarding)
- Line accumulation provides operational visibility (logging)
- Separation of concerns: forwarding is critical, logging is best-effort
- MCP clients still perform their own line-buffering on stdin

### 6.2 Text channel

Text WebSocket messages are reserved for out-of-band metadata.

Typical message types:
- `server_started`
- `server_exited`
- `stderr`
- `error`

Example:

```json
{"type":"server_started","pid":1234,"ts":"2026-04-14T23:00:00Z"}
{"type":"stderr","data":"failed to load config\n","ts":"2026-04-14T23:00:01Z"}
{"type":"server_exited","code":0,"signal":null,"ts":"2026-04-14T23:00:10Z"}
```

This channel is private to the proxy design and is not part of MCP itself.

## 7. MCP stream behavior

### 7.1 stdout rules

The MCP stdio transport requires stdout to contain only protocol messages.
Therefore:
- the local proxy must never write diagnostics to stdout,
- the remote proxy must never inject diagnostics into the binary MCP stream,
- any non-MCP data must use stderr or the out-of-band text channel.

### 7.2 stderr rules

MCP allows stderr to be used for logs and explicitly treats capture/forwarding of stderr as optional behavior for clients.
This design therefore treats remote server stderr forwarding as a **feature flag**, not a protocol requirement.

**Key design decision:** stderr forwarding is **non-fatal** - errors in stderr handling never kill the MCP connection. The stdin/stdout channel (the actual MCP protocol) continues operating even if stderr forwarding fails.

## 8. stderr policy

### 8.1 Remote proxy

The remote proxy:
- captures the server's stderr,
- logs it locally through its own structured logging system,
- forwards it to the local proxy via text WebSocket messages.

**stderr worker resilience:**
- Uses `bufio.Reader` (not `Scanner`) to handle arbitrarily long lines
- Lines >1MB are skipped with a warning (prevents log flooding)
- Read errors, WebSocket send errors → logged but **reading continues**
- Only exits on **EOF** (process closed stderr) or **context cancellation**
- **Never triggers connection shutdown** - stderr is diagnostic, not protocol

The remote proxy logs its own operational events using Go's `slog` structured logging package.

**Logging features:**
- **Structured output**: All logs are structured for easy parsing and filtering
- **Adaptive format**:
  - When running under systemd: automatically uses journald format
  - When stderr is a TTY: uses human-readable text format
  - Otherwise: uses JSON format for machine parsing
- **Configurable log levels**: `DEBUG`, `INFO`, `WARN`, `ERROR` (default: `INFO`)
- **Source tracking**: Debug mode includes source file and line numbers
- **Invocation tracking**: Under systemd, includes invocation ID for session correlation

**Configuration:**
- `--log-level LEVEL` or `-l LEVEL`: Set the minimum log level to emit

The remote proxy may also log operational events independently of the spawned MCP server.

### 8.2 Local proxy

The local proxy writes its own operational logs to local stderr, and re-emits remote server stderr to local stderr.

**Forwarded server stderr** is prefixed with `[SERVER]` to distinguish it from local proxy output.

**Local proxy output** (operational messages, lifecycle notifications, errors) is written without prefixes, as it originates from the local process itself and is inherently distinguishable from forwarded remote server output.

## 10. Compression

### 10.1 Mechanism

Compression uses standard WebSocket `permessage-deflate`, negotiated during the WebSocket handshake. 
No application-level gzip/deflate is added to binary payloads.

### 10.2 Granularity

Because the transport uses **1 MCP line = 1 WebSocket message**, compression is naturally applied per MCP message.

### 10.3 Context takeover

Preferred mode:
- **CompressionContextTakeover**

Reason:
- MCP/JSON-RPC traffic contains many repeated field names and repeated message shapes across a session,
- context takeover preserves the DEFLATE sliding window across messages and usually gives a significantly better ratio on repetitive session traffic.

`CompressionNoContextTakeover` is acceptable if memory usage per connection becomes a concern, but it is not the preferred default for this project.

## 11. Keepalive and dead peer detection

WebSocket ping/pong is used to detect dead peers and remote crashes.

Recommended defaults:
- ping interval: 15s
- pong timeout: 30s

If the remote proxy does not receive pong responses in time:
1. it treats the session as dead,
2. it closes server stdin,
3. it escalates to SIGTERM/SIGKILL if needed.

This is important because a broken network session does not always map cleanly to immediate TCP teardown.

## 12. Authentication and encryption (TLS mode only)

TLS authentication is **optional**. It should be used when the connection traverses untrusted networks. When running over a VPN or trusted isolated network, plain WebSocket (`ws://`) without TLS is acceptable.

### 12.1 TLS model

When TLS is enabled, the transport uses TLS for confidentiality and integrity.  
Authentication can use **mutual TLS (mTLS)**:
- server presents a server certificate,
- client presents a client certificate (optional, can be server-only TLS).

This is preferred over bearer tokens for a fixed set of developer machines and personal servers.

### 12.2 Certificate roles

Certificates use X.509 Extended Key Usage:
- server certificate: `serverAuth`
- client certificate: `clientAuth`

This prevents a leaked client certificate from being reused as a fake server certificate in a MITM scenario, and vice versa, assuming both sides enforce role checks.

**Authentication Model:**
- Client authenticates server based on: (1) valid CA signature, (2) presence of `serverAuth` EKU
- Server authenticates client based on: (1) valid CA signature, (2) presence of `clientAuth` EKU
- **Hostname/IP verification is NOT part of the security model** — certificates do not require Subject Alternative Names (SAN)
- Trust is established through CA chain validation and EKU role separation, not endpoint identity

### 12.3 Simple PKI model

To keep the system simple for personal use:
- one command generates a fresh CA,
- one server cert/key pair is generated,
- one client cert/key pair is generated,
- the CA private key is **not persisted**.

Consequences:
- there is no revocation infrastructure,
- rotation is achieved by generating a new complete bundle,
- users are responsible for protecting their private keys,
- if compromise is suspected, regenerate and redistribute everything.

**Security Implications:**
- Since the CA private key exists only in memory during generation and is never written to disk:
  - no intermediate CA certificates can be created after the generation command completes,
  - `MaxPathLen` constraint is optional (defense-in-depth only),
  - certificate chain validation at runtime is unnecessary if generation is tested.
- The closed trust bundle means: only certificates generated in the same run are mutually trusted.

This is intentional and appropriate for a non-commercial self-hosted tool.

## 13. Certificate generation model (TLS mode only)

Certificate generation is only required when using TLS transport (`wss://`).  
When using plain WebSocket (`ws://`) over a VPN or trusted network, certificates are not needed.

The binary may expose a command like:

```text
mcptp gencerts
```

Expected outputs:
- `ca.crt`
- `server.crt`
- `server.key`
- `client.crt`
- `client.key`

Explicitly **not** written:
- `ca.key`

**Certificate Properties:**
- CA certificate: `IsCA=true`, `KeyUsage=CertSign`, no SAN required
- Server certificate: `serverAuth` EKU, no SAN required (hostname verification disabled in TLS config)
- Client certificate: `clientAuth` EKU, no SAN required
- All certificates: Ed25519 keys (or equivalent modern algorithm)
- Validity period: ~10 years (rotation is manual bundle regeneration)

**Revocation:**
- No CRL or OCSP infrastructure
- Compromise response: regenerate entire bundle and redistribute
- `KeyUsage=CRLSign` on CA is optional (not needed for this revocation model)

This creates a closed trust bundle:
- only the generated client and server certs are valid,
- no later certificates can be signed from the same CA because the CA private key no longer exists,
- rotation is done by re-running generation.

## 14. EKU verification policy

Because EKU is an extension rather than a primitive transport field, role checking should be implemented explicitly rather than left implicit.

### 14.1 Client side

The client verifies:
- server chain validity (signed by trusted CA),
- presence of `serverAuth` EKU.

**Note:** Hostname/SAN verification is explicitly **disabled** — authentication is based on CA signature + EKU, not endpoint identity.

### 14.2 Server side

The server verifies:
- client chain validity (signed by trusted CA),
- presence of `clientAuth` EKU.

If using Go's verification hooks, verified chains are represented as `[][]*x509.Certificate`, where:
- first index = candidate verified chain,
- second index = certificate position inside that chain,
- `[0][0]` is the leaf certificate presented by the peer.

So role checks typically inspect:

```go
cert := verifiedChains[0][0]
```

### 14.3 TLS Configuration Requirements

**Client-Side TLS Configuration:**
- Set `RootCAs` to the generated CA certificate
- Set `ClientAuthCert` to the generated client certificate/key pair
- Set `ServerName` to empty string OR set `InsecureSkipVerify=true` (hostname verification disabled by design)

**Server-Side TLS Configuration:**
- Set `ClientCAs` to the generated CA certificate
- Set `ClientAuth` to `RequireAndVerifyClientCert`
- Set `Certificates` to the generated server certificate/key pair
- No hostname verification on client certificates

**EKU Verification:**
- Go's `crypto/tls` automatically validates EKU when `ClientAuth` is set
- Implementations should verify this behavior in tests

## 15. Concurrency model

### 15.1 Default

The default server model is:
- multiple connections allowed,
- each connection gets its own spawned MCP server process.

### 15.2 Server limitations

Some MCP servers cannot safely support concurrent use of the same config/profile directory and require complete per-process isolation.  
This is not a proxy problem; it is a server property.

The proxy cannot turn a stateful single-session server into a multi-session shared backend.  
MCP sessions are stateful and carry initialization state and conversational state across messages.

### 15.3 Server protection flag

Recommended server configuration:
- `--max-connections=0` for unlimited independent spawns
- `--max-connections=1` for servers that should only run one session at a time

This is effectively the generalization of a `--single-session` flag.

Recommended overload policy:
- reject new sessions rather than queue them

Reason:
- simpler implementation,
- no risk of long or stuck queueing,
- immediate feedback to the user.

## 16. Stateful MCP servers

If an MCP server requires one profile/config directory to be used by only one process at a time, the remote proxy must respect that limitation operationally.  
The proxy can protect users by limiting concurrency, but it cannot change the server's state model.

For such servers, recommended deployment is:
- `--max-connections=1`, or
- one isolated profile/home per spawned process if the server supports that pattern.

## 17. Local and remote responsibilities

### 17.1 Local proxy responsibilities

- behave exactly like a stdio MCP server,
- connect to remote over secure WebSocket,
- forward MCP binary messages,
- forward remote server stderr to local stderr,
- never write anything except MCP messages to stdout.

### 17.2 Remote proxy responsibilities

- authenticate the client,
- spawn the real MCP server,
- wire stdin/stdout/stderr,
- forward MCP binary messages,
- forward stderr via text channel,
- kill/clean up the process on disconnect,
- enforce concurrency limits.

### 17.3 Server responsibilities

- speak MCP stdio correctly,
- read stdin,
- write MCP JSON-RPC messages to stdout,
- treat EOF on stdin as normal session termination,
- write logs to stderr if desired.

## 18. Configuration summary

### Local proxy

Suggested options:
- `--remote ws(s)://host:port/path` - WebSocket URL (required). Use `ws://` for plain mode or `wss://` for TLS mode.
- `--log-level LEVEL` or `-l LEVEL` - Set minimum log level (`DEBUG`, `INFO`, `WARN`, `ERROR`). Default: `INFO`.
- `--ca path/to/ca.crt` (TLS mode only)
- `--client-cert path/to/client.crt` (TLS mode only, for mTLS)
- `--client-key path/to/client.key` (TLS mode only, for mTLS)

### Remote proxy

Suggested options:
- `--bind ADDRESS` - Address to bind to (e.g., `:7777`, `0.0.0.0`, `::`). Required.
- `--port PORT` - Port to bind to (e.g., `7777`). Optional, can be included in `--bind` as `:PORT`. Default: 8623.
- `--log-level LEVEL` or `-l LEVEL` - Set minimum log level (`DEBUG`, `INFO`, `WARN`, `ERROR`). Default: `INFO`.
- `--ca path/to/ca.crt` (TLS mode only)
- `--server-cert path/to/server.crt` (TLS mode only)
- `--server-key path/to/server.key` (TLS mode only)
- `--max-connections N`
- `--ping-interval 15s`
- `--pong-timeout 30s`
- `--server /path/to/mcp-server`
- `--` followed by server args

For plain mode (no TLS), omit the certificate-related options.

### Certificate generation (TLS mode only)

Suggested option:
- `mcptp gencerts --out ./certs`

## 19. Recommended defaults

For this project, the recommended defaults are:

- transport: `wss://` for public networks, `ws://` acceptable over VPN/trusted networks
- authentication: mTLS when using TLS, none required for plain mode
- cert generation: one-shot CA/server/client bundle (TLS mode only)
- CA key persistence: disabled
- hostname verification: **disabled** (authentication via CA+EKU, not endpoint identity)
- SAN on certificates: **not required**
- certificate rotation: regenerate full bundle and redistribute
- compression: `permessage-deflate`
- compression mode: context takeover
- MCP framing: one line per binary message
- stderr forwarding: enabled
- JSON-RPC inspection: enabled optionally, read-only
- max connections: unlimited by default, configurable to `1`
- overload behavior: reject
- log level: `INFO` by default, `DEBUG` for debug mode (includes source tracking)

## 20. Final design statement

This proxy is intentionally a thin remote-exec bridge for MCP stdio, not a session broker, not a shared multi-user backend, and not a full PKI product.  
Its purpose is to preserve the local client mental model while moving the actual MCP server execution to a remote machine. TLS/mTLS provides secure transport over untrusted networks, but can be disabled when running over VPNs or trusted tunnels for simpler deployment.

**The mTLS implementation prioritizes simplicity over flexibility:**
- closed trust bundle (no external CAs, no intermediate CAs),
- no hostname-based authentication (CA signature + EKU only),
- no revocation infrastructure (full rotation on compromise),
- ephemeral CA key (cannot issue new certs after generation).

**This is appropriate for:** single developers, small teams, personal servers, self-hosted deployments.  
**This is NOT appropriate for:** multi-tenant systems, enterprise PKI requirements, certificate-based access control at scale.