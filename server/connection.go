package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
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

	// Prepare to close the web socket connection at the end of the function
	var (
		err          error
		desiredClose *websocket.CloseError
	)
	defer func() {
		if desiredClose == nil {
			// instructed to not close properly the websocket
			return
		}
		if err = wsc.Close(desiredClose.Code, desiredClose.Reason); err != nil {
			logger.Error("failed to close websocket",
				slog.Any("error", err),
				slog.Any("desired_code", desiredClose.Code),
				slog.String("desired_reason", desiredClose.Reason),
			)
		} else {
			logger.Info("websocket closed properly",
				slog.Int("close_code", int(desiredClose.Code)),
				slog.String("close_reason", desiredClose.Reason),
			)
		}
	}()

	// Prepare process execution
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
	if err = subProcess.Start(); err != nil {
		logger.Error("failed to start process", slog.Any("error", err))
		// Send start error to client
		err = protocol.OoBMessageStartErrPayload{
			Error: err.Error(),
		}.SendWebSocketOoBMessage(processCtx, wsc)
		if err != nil {
			logger.Error("failed to send start error message", slog.Any("error", err))
		}
		// Close the connection on exit
		desiredClose = new(websocket.CloseError{
			Code:   websocket.StatusInternalError,
			Reason: "process failed to start",
		})
		return
	}
	err = protocol.OoBMessageStartOKPayload{
		PID: subProcess.Process.Pid,
	}.SendWebSocketOoBMessage(processCtx, wsc)
	if err != nil {
		logger.Error("failed to send start ok message",
			slog.Any("error", err),
		)
		// Close the connection on exit
		desiredClose = new(websocket.CloseError{
			Code:   websocket.StatusInternalError,
			Reason: "failed to send start msg",
		})
		return
	}
	logger.Info("process started",
		slog.String("command", strings.Join(mcpServerCmdline, " ")),
		slog.Int("pid", subProcess.Process.Pid),
	)

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

	// Wait for either worker to finish first. The first exit determines the cleanup procedure.
	select {
	case desiredClose = <-receiverChan:
		// Receiver finished first, stop process and end websocket connection
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
			// Before closing, make sure the sender had time to drain stdout/stderr and sent them (but discard its own errors if any)
			logger.Debug("waiting for sender to finish")
			<-senderChan
			// Send error to client
			var (
				exitErr *exec.ExitError
				msg     protocol.OoBMessageServerExitedPayload
			)
			if errors.As(err, &exitErr) {
				if exitErr.Exited() {
					msg.ExitCode = new(exitErr.ExitCode())
				} else {
					msg.Killed = true
				}
			}
			msg.Error = err.Error()
			if err = msg.SendWebSocketOoBMessage(processCtx, wsc); err != nil {
				logger.Error("failed to send stop error to client",
					slog.Any("send_error", err),
					slog.Any("stop_error", msg),
				)
			} else {
				logger.Debug("sent stop error to client",
					slog.Any("stop_error", msg),
				)
			}
			// Adapt returned closing reason
			if desiredClose == nil {
				// receiver indicates an issue with websocket connection, to not bother trying to send error and close properly the websocket
				return
			}
			if desiredClose.Code == websocket.StatusNormalClosure {
				// We were about to send a normal closure: adapt closing to wait error
				desiredClose = &websocket.CloseError{
					Code:   websocket.StatusInternalError,
					Reason: "process did not finish gracefully",
				}
				return
			}
			// We were not about to send a normal closure: close with original error instead (desiredClose already set)
			return
		}
		// Process finished normally: wait for sender to finish (drain stdout/stderr and send it)
		logger.Debug("process finished normally, waiting for sender to finish")
		<-senderChan
		// (try to) send exit message
		msg := protocol.OoBMessageServerExitedPayload{
			ExitCode: new(0),
		}
		if msg.SendWebSocketOoBMessage(processCtx, wsc); err != nil {
			logger.Error("failed to send process exit message",
				slog.Int("exit_msg", *msg.ExitCode),
				slog.Bool("killed", msg.Killed),
				slog.Any("error", err),
			)
		} else {
			logger.Debug("exit message successfully sent",
				slog.Int("exit_code", *msg.ExitCode),
				slog.Bool("killed", msg.Killed),
			)
		}
		// desiredClose already ready for closure (nil or not), we are done
		return
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
		return
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
			// Handle OoB message
			oobMsg, err := protocol.ReadWebSocketOoBMessage(msg)
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
				logger.Warn("unknown OoB message",
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
