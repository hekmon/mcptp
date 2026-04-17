package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
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
	processCtx, processCancel := context.WithCancel(context.Background())
	defer processCancel()

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
	receiverChan := make(chan *websocket.CloseError, 1)
	receiverContext, receiverContextCancel := context.WithCancel(processCtx)
	defer receiverContextCancel()
	go func() {
		receiverChan <- receiver(receiverContext, wsc, subProcessStdinWriter, logger.With("component", "receiver"))
	}()
	senderChan := make(chan *websocket.CloseError, 1)
	// senderContext, senderContextCancel := context.WithCancel(processCtx)
	// defer senderContextCancel()
	go func() {
		senderChan <- sender(processCtx, wsc, subProcessStdoutReader, subProcessStderrReader, logger.With("component", "sender"))
	}()

	// Wait for either worker to finish first. The first exit determines the root cause:
	// - receiver first: client initiated shutdown (normal) or websocket issue → use closeWith to close websocket
	// - sender first: process died unexpectedly → cancel context, wait for receiver to finish (ignore its error)
	var desiredClose *websocket.CloseError
	select {
	case desiredClose = <-receiverChan:
		// Receiver finished first → close websocket with its instructions
		logger.Debug("receiver finished first: stopping process",
			slog.Int("desired_close_code", int(desiredClose.Code)),
			slog.String("desired_close_reason", desiredClose.Reason),
			slog.Duration("wait_grace_period", subProcess.WaitDelay),
		)
		// Stop process first
		subProcess.WaitDelay = protocol.SubProcessGracePeriod
		if err = subProcess.Wait(); err != nil {
			logger.Error("process did not finish gracefully",
				slog.Any("error", err),
			)
			// Before closing, make sure the sender had time to drain stdout/stderr and sent it (but discard its own errors)
			logger.Debug("waiting for sender to finish")
			<-senderChan
			// Adapt returned closing reason
			if desiredClose == nil {
				// receiver indicates an issue with websocket connection, to not bother trying to send error
			} else if desiredClose.Code != websocket.StatusNormalClosure {
				// We were not about to send a normal closure → send original error
			} else {
				// TODO - we were about to send a normal closure → adapt closing to wait error
			}
		} else {
			// Process finished normally: wait for sender to finish (drain stdout/stderr and send it)
			logger.Debug("process finished normally, waiting for sender to finish")
			<-senderChan
			// Adapt returned closing reason
			if desiredClose == nil {
				// receiver indicates an issue with websocket connection, to not bother trying to send error
			} else {
				// TODO - we were about to send a closure, and the process exited cleanly, let's proceed
			}
		}
		// TODO - wait for sender to finish (drain stdout/stderr)
	case desiredClose = <-senderChan:
		// Sender finished first
		logger.Debug("sender finished first: cancelling receiver context and wait for it to finish",
			slog.Int("desired_close_code", int(desiredClose.Code)),
			slog.String("desired_close_reason", desiredClose.Reason),
		)
		receiverContextCancel()
		<-receiverChan // error will most likely be context canceled, discard it
		// Wait on program state to have its process state
		// TODO
	}
	return // hide following that must be adapted to the new select upper
	// Wait for the process to finish with a timeout and build exit payload
	subProcess.WaitDelay = protocol.SubProcessGracePeriod
	waitErr := subProcess.Wait()
	processCancel() // free the other worker, now that process has finished
	// Build server_exited OoB payload based on exit status
	var (
		exitPayload protocol.OoBMessageServerExitedPayload
		exitErr     *exec.ExitError
	)
	switch {
	case waitErr == nil:
		// Clean exit - retrieve actual exit code (should be 0, but don't assume)
		code := subProcess.ProcessState.ExitCode()
		exitPayload = protocol.OoBMessageServerExitedPayload{
			ExitCode: &code,
			Killed:   false,
		}
		if code != 0 {
			logger.Warn("MCP server exited cleanly but with non-zero code",
				slog.Int("exit_code", code),
			)
		} else {
			logger.Info("MCP server exited cleanly")
		}
	case errors.As(waitErr, &exitErr):
		// Process exited with error - get exit code (works cross-platform)
		code := exitErr.ExitCode()
		logger.Warn("MCP server exited with error",
			slog.Int("exit_code", code),
		)
		exitPayload = protocol.OoBMessageServerExitedPayload{
			ExitCode: &code,
			Killed:   false,
		}
	case errors.Is(waitErr, exec.ErrWaitDelay):
		// WaitDelay expired - process was force-killed
		logger.Warn("MCP server did not exit gracefully (killed after timeout)",
			slog.Duration("grace_period", protocol.SubProcessGracePeriod),
		)
		exitPayload = protocol.OoBMessageServerExitedPayload{
			Killed: true,
		}
	default:
		// Unexpected I/O or system error (no exit code available)
		logger.Error("MCP server wait failed", slog.Any("error", waitErr))
		exitPayload = protocol.OoBMessageServerExitedPayload{
			Killed: true,
		}
	}
	// Send OoB message if the websocket is still open
	if desiredClose != nil {
		if msg, err := exitPayload.WebSocketMessage(); err != nil {
			logger.Error("failed to marshal server_exited message", slog.Any("error", err))
		} else if writeErr := wsc.Write(processCtx, websocket.MessageText, msg); writeErr != nil {
			// Websocket might already be closed - don't treat as fatal
			logger.Error("failed to send server_exited (client may be disconnected)",
				slog.Any("error", writeErr),
			)
		}
	}
	// Finaly, close the websocket with the appropriate code
	if err = wsc.Close(desiredClose.Code, desiredClose.Reason); err != nil {
		logger.Error("failed to close websocket properly", slog.Any("error", err))
	}
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

func sender(ctx context.Context, wsc *websocket.Conn, processStdout, processStderr io.Reader, logger *slog.Logger) (closeWith *websocket.CloseError) {
	// TODO
	return nil
}
