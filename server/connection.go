package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/hekmon/mcproxy/protocol"

	"github.com/coder/websocket"
)

var (
	connIDs atomic.Int64
	nbConn  atomic.Int64
)

func incomingConnection(w http.ResponseWriter, r *http.Request) {
	// Create a sub logger with connection ID for this connection
	logger := logger.With(
		slog.Int64("connection_id", connIDs.Add(1)),
	)
	logger.Info("received new connection")
	// check if we have reached max connections if set
	totalConnections := nbConn.Add(1)
	defer nbConn.Add(-1)
	if maxConnections > 0 && totalConnections > maxConnections {
		logger.Error("refusing request: too many connections",
			slog.Int64("total_connections", totalConnections),
			slog.Int64("max_connections", maxConnections),
		)
		http.Error(w,
			fmt.Sprintf("Too many connections: %d > %d\n", totalConnections, maxConnections),
			http.StatusTooManyRequests,
		)
		return
	}
	// switch to web socket
	wsc, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: protocol.CompressionMode,
	})
	if err != nil {
		logger.Error("failed to initialize websocket", slog.Any("error", err))
		return
	}
	defer func() {
		if err = wsc.CloseNow(); err != nil {
			logger.Error("failed to close websocket", slog.Any("error", err))
		}
	}()
	// once the connection is established, hand over the proxy
	proxy(wsc, logger)
}

func proxy(wsc *websocket.Conn, logger *slog.Logger) {
	// Set a process run global context
	processCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Prepare process
	subProcess := exec.CommandContext(processCtx, mcpServerCmdline[0], mcpServerCmdline[1:]...)
	var (
		subProcessStdinWriter  *io.PipeWriter
		subProcessStdoutReader *io.PipeReader
		subProcessStderrReader *io.PipeReader
	)
	subProcess.Stdin, subProcessStdinWriter = io.Pipe()
	subProcessStdoutReader, subProcess.Stdout = io.Pipe()
	subProcessStderrReader, subProcess.Stderr = io.Pipe()
	// Start the process
	if err := subProcess.Start(); err != nil {
		logger.Error("failed to start process", slog.Any("error", err))
		// Send error to client
		msg, err := protocol.OoBMessageStartErrPayload{
			Error: err.Error(),
		}.WebSocketMessage()
		if err != nil {
			logger.Error("failed to marshal error message", slog.Any("error", err))
			return
		}
		if err = wsc.Write(processCtx, websocket.MessageText, msg); err != nil {
			logger.Error("failed to send error message", slog.Any("error", err))
		}
		return
	}
	msg, err := protocol.OoBMessageStartOKPayload{
		PID: subProcess.Process.Pid,
	}.WebSocketMessage()
	if err != nil {
		logger.Error("failed to marshal start ok message", slog.Any("error", err))
		return
	}
	if err = wsc.Write(processCtx, websocket.MessageText, msg); err != nil {
		logger.Error("failed to send start ok message", slog.Any("error", err))
		return
	}
	// Start the IO workers
	// Note: We use sync.WaitGroup + channels instead of errgroup.WithContext() to avoid
	// premature context cancellation. When one worker exits, the other must continue
	// until it completes its task naturally (e.g., sender reads stdout until EOF).
	// Error variables are written before their done channel is closed, so reading them
	// after <-doneChannel is safe (channel close provides memory barrier).
	var (
		workers     sync.WaitGroup
		receiverErr *websocket.CloseError // Safe to read after <-receiverDone
		senderErr   error                 // Safe to read after <-senderDone
	)
	receiverDone := make(chan struct{})
	workers.Add(1)
	go func() {
		receiverErr = receiver(processCtx, wsc, subProcessStdinWriter, logger.With("component", "receiver"))
		close(receiverDone)
	}()
	senderDone := make(chan struct{})
	workers.Add(1)
	go func() {
		senderErr = sender(processCtx, wsc, subProcessStdoutReader, subProcessStderrReader, logger.With("component", "sender"))
		close(senderDone)
	}()
	// Wait for either worker to finish first. The first exit determines the root cause:
	// - receiver first: client initiated shutdown (normal) or websocket error → use closeWith to close websocket
	// - sender first: process died unexpectedly → cancel context, wait for receiver to finish (ignore its error)
	// The second worker will likely fail due to context/process cancellation—this is collateral damage, not
	// the root cause. We wait for it to finish cleanly but ignore its error.
	select {
	case <-receiverDone:
		// Receiver exited first: use closeWith to close websocket if non-nil
		// If nil, websocket already dead, skip close
		// Sender will finish when process exits (stdout EOF)

	case <-senderDone:
		// Sender exited first: process died unexpectedly
		// Cancel context to stop receiver, wait for it to finish, ignore its error

	}
	// Wait for the process to finish with a timeout
	subProcess.WaitDelay = protocol.SubProcessGracePeriod
	if err := subProcess.Wait(); err != nil {
		logger.Error("process failed", slog.Any("error", err))
	}
	// Wait for the workers to properly finish
	workers.Wait()
}

// receiver reads from websocket and writes to process stdin.
// Return value semantics (closeWith):
//   - *CloseError: caller should close websocket with these codes (instructions for how TO close)
//   - nil: websocket already closed/dead, no need to close
func receiver(ctx context.Context, wsc *websocket.Conn, processStdin io.WriteCloser, logger *slog.Logger) (closeWith *websocket.CloseError) {
	// Signal the subprocess we won't be sending any more data to it after this function returns (MCP signal for clean shutdown)
	defer processStdin.Close()
	// Start the read/write loop
	var (
		msgType websocket.MessageType
		msg     []byte
		err     error
	)
	wsc.SetReadLimit(-1) // disable read limit
	for {
		// Read a websocket message
		if msgType, msg, err = wsc.Read(ctx); err != nil {
			statusCode := websocket.CloseStatus(err)
			switch statusCode {
			case -1:
				// Error is not a websocket close error
				logger.Error("failed to read websocket message", slog.Any("error", err))
				return &websocket.CloseError{
					Code:   websocket.StatusInternalError,
					Reason: fmt.Sprintf("failed to read websocket message: %s", err),
				}
			default:
				// Websocket closed
				logger.Warn("websocket connection closed without OoB shutdown received",
					slog.Int("status_code", int(statusCode)),
					slog.String("status_text", statusCode.String()),
				)
				return nil // no need to close the websocket, it's already closed
			}
		}
		// Handle the message
		switch msgType {
		case websocket.MessageBinary:
			// TODO log JSON-RPC message
			if _, err = processStdin.Write(msg); err != nil {
				logger.Error("failed to write to process stdin", slog.Any("error", err))
				return &websocket.CloseError{
					Code:   websocket.StatusInternalError,
					Reason: fmt.Sprintf("failed to write to process stdin: %s", err),
				}
			}
		case websocket.MessageText:
			// Unmarshal OoB message
			var oobMessage protocol.OoBMessage
			if err = json.Unmarshal(msg, &oobMessage); err != nil {
				logger.Error("failed to unmarshal OoB message", slog.Any("error", err))
				return &websocket.CloseError{
					Code:   websocket.StatusUnsupportedData,
					Reason: fmt.Sprintf("failed to unmarshal OoB message: %s", err),
				}
			}
			// Handle OoB message
			oobMsg, err := oobMessage.WebSocketMessagePayload()
			if err != nil {
				logger.Error("failed to extract OoB message payload", slog.Any("error", err))
				return &websocket.CloseError{
					Code:   websocket.StatusUnsupportedData,
					Reason: fmt.Sprintf("failed to extract OoB message payload: %s", err),
				}
			}
			switch typedMsg := oobMsg.(type) {
			case protocol.OoBMessageShutdownPayload:
				logger.Info("shutdown message received, closing stdin")
				return &websocket.CloseError{
					Code:   websocket.StatusNormalClosure,
					Reason: "shutdown message acknowledged",
				}
			default:
				if oobMessage.Type == "" {
					logger.Error("unexpected text message received",
						slog.String("msg", string(msg)),
					)
					return &websocket.CloseError{
						Code:   websocket.StatusUnsupportedData,
						Reason: "unexpected text message received",
					}
				}
				logger.Warn("unknown OoB message type",
					slog.String("type", string(oobMessage.Type)),
					slog.Any("payload", typedMsg),
				)
				// continue
			}
		default:
			// should never happen
			logger.Error("unexpected websocket message type", slog.Any("type", msgType))
			return &websocket.CloseError{
				Code:   websocket.StatusUnsupportedData,
				Reason: "unexpected websocket message type",
			}
		}
	}
}

func sender(ctx context.Context, wsc *websocket.Conn, processStdout, processStderr io.Reader, logger *slog.Logger) (err error) {
	// TODO
	return nil
}
