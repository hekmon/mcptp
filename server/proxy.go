package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hekmon/mcptp/protocol"

	"github.com/coder/websocket"
)

var (
	connIDs atomic.Int64
	nbConn  atomic.Int64
)

func handleConnection(runningCtx context.Context, inflight *sync.WaitGroup) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Done()
		// Create a sub logger with connection ID for this connection
		logger := logger.With(
			slog.Int64("connection_id", connIDs.Add(1)),
		)
		logger.Info("received new connection")
		// check if we have reached max connections if set
		if maxConnections > 0 {
			totalConnections := nbConn.Add(1)
			defer nbConn.Add(-1)
			if totalConnections > maxConnections {
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
			// safety net in case the proxy function does not properly close the connection
			if err = wsc.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("failed to close websocket", slog.Any("error", err))
			}
		}()
		// once the websocket connection is established, hand over the proxy
		proxy(runningCtx, wsc, logger)
	}
}

func proxy(runningCtx context.Context, wsc *websocket.Conn, logger *slog.Logger) {
	// Set a process run global context
	processCtx, processCancel := context.WithCancel(runningCtx)
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
	logger.Debug("starting process", slog.Any("cmdline", mcpServerCmdline))
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
	// Set WaitDelay unconditionally so it applies to all shutdown paths
	subProcess.WaitDelay = protocol.SubProcessGracePeriod
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
	receiverStdinChan := make(chan *websocket.CloseError, 1)
	receiverStdinContext, receiverStdinContextCancel := context.WithCancel(processCtx)
	defer receiverStdinContextCancel()
	go receiverStdin(receiverStdinContext, wsc, subProcessStdinWriter, logger.With("component", "receiver_stdin"), receiverStdinChan)
	senderStdoutChan := make(chan *websocket.CloseError, 1)
	senderStdoutContext, senderStdoutContextCancel := context.WithCancel(processCtx)
	defer senderStdoutContextCancel()
	go senderStdout(senderStdoutContext, wsc, subProcessStdoutReader, logger.With("component", "sender_stdout"), senderStdoutChan)
	senderStderrChan := make(chan struct{}, 1)
	senderStderrContext, senderStderrContextCancel := context.WithCancel(processCtx)
	defer senderStderrContextCancel()
	go senderStderr(senderStderrContext, wsc, subProcessStderrReader, logger.With("component", "sender_stderr"), senderStderrChan)

	// Wait for either stdin or stdout worker to finish first. The first exit determines the cleanup procedure.
	// stderr is excluded from this select as it's optional and should not trigger shutdown.
	select {
	case desiredClose = <-receiverStdinChan:
		// Receiver finished first: stopping process and end websocket connection
		logger.Debug("receiver finished first: stopping process",
			slog.Int("desired_close_code", int(desiredClose.Code)),
			slog.String("desired_close_reason", desiredClose.Reason),
			slog.Duration("wait_grace_period", subProcess.WaitDelay),
		)
		// Stop process first
		if err = subProcess.Wait(); err != nil {
			// Drain stdout/stderr senders before handling error, but let them finish (and send potential remaining buffer first)
			logger.Debug("draining stdout sender before handling error")
			senderStdoutContextCancel()
			<-senderStdoutChan
			logger.Debug("draining stderr sender before handling error")
			senderStderrContextCancel()
			<-senderStderrChan
			handleProcessWaitError(processCtx, wsc, logger, err, &desiredClose)
			return
		}
		// Process finished normally: drain stdout/stderr senders and send exit message
		logger.Debug("draining stdout sender after normal process exit")
		senderStdoutContextCancel()
		<-senderStdoutChan
		logger.Debug("draining stderr sender after normal process exit")
		senderStderrContextCancel()
		<-senderStderrChan
		sendNormalExitMessage(processCtx, wsc, logger)
		// desiredClose already ready for closure (nil or not), we are done
		return
	case desiredClose = <-senderStdoutChan:
		logger.Debug("stdout sender failed, cancel stdin receiver and drain stderr sender")
		receiverStdinContextCancel()
		<-receiverStdinChan
		// Drain stderr (already exited or just exiting now)
		senderStderrContextCancel()
		<-senderStderrChan
		// Wait for process exit and send appropriate message
		if err = subProcess.Wait(); err != nil {
			handleProcessWaitError(processCtx, wsc, logger, err, &desiredClose)
			return
		}
		// else program finished gracefully, let's use the original desired close error on return
		sendNormalExitMessage(processCtx, wsc, logger)
	}
}

// handleProcessWaitError handles the error path when subProcess.Wait() fails.
// It logs the error, sends an error message to the client, and adjusts desiredClose if needed.
func handleProcessWaitError(ctx context.Context, wsc *websocket.Conn, logger *slog.Logger, err error, desiredClose **websocket.CloseError) {
	logger.Error("process did not finish gracefully", slog.Any("error", err))
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
	if sendErr := msg.SendWebSocketOoBMessage(ctx, wsc); sendErr != nil {
		logger.Error("failed to send stop error to client",
			slog.Any("send_error", sendErr),
			slog.Any("stop_error", msg),
		)
	} else {
		logger.Debug("sent stop error to client",
			slog.Any("stop_error", msg),
		)
	}
	// Adapt returned closing reason
	if *desiredClose == nil {
		return // websocket issue, don't bother closing properly
	}
	if (*desiredClose).Code == websocket.StatusNormalClosure {
		*desiredClose = &websocket.CloseError{
			Code:   websocket.StatusInternalError,
			Reason: "process did not finish gracefully",
		}
	}
	// Otherwise keep original desiredClose
}

// sendNormalExitMessage sends a normal exit message (exit code 0) to the client.
func sendNormalExitMessage(ctx context.Context, wsc *websocket.Conn, logger *slog.Logger) {
	msg := protocol.OoBMessageServerExitedPayload{
		ExitCode: new(0),
	}
	if err := msg.SendWebSocketOoBMessage(ctx, wsc); err != nil {
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
}

// receiver reads from websocket and writes to process stdin.
// Return value semantics (closeWith):
//   - *CloseError: caller should close websocket with these codes (instructions for how TO close)
//   - nil: websocket already closed/dead, no need to close
func receiverStdin(ctx context.Context, wsc *websocket.Conn, processStdin *io.PipeWriter, logger *slog.Logger, returnErr chan<- *websocket.CloseError) {
	// Signal the subprocess we won't be sending any more data to it after this function returns (MCP signal for clean shutdown)
	defer processStdin.Close()
	// Start the read/write loop
	var (
		msgType websocket.MessageType
		msg     []byte
		err     error
		n       int
	)
	wsc.SetReadLimit(protocol.BinaryFrameMaxSize)
	for {
		// Read a websocket message
		if msgType, msg, err = wsc.Read(ctx); err != nil {
			statusCode := websocket.CloseStatus(err)
			switch statusCode {
			case -1:
				// Error is not a websocket close error
				logger.Error("failed to read websocket message", slog.Any("error", err))
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusInternalError,
					Reason: fmt.Sprintf("failed to read websocket message: %s", err),
				}
				return
			default:
				// Websocket closed
				logger.Warn("websocket connection closed without OoB shutdown received",
					slog.Int("status_code", int(statusCode)),
					slog.String("status_text", statusCode.String()),
				)
				returnErr <- nil // no need to close the websocket, it's already closed
				return
			}
		}
		// Handle the message
		switch msgType {
		case websocket.MessageBinary:
			if n, err = processStdin.Write(msg); err != nil {
				logger.Error("failed to write to process stdin", slog.Any("error", err))
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusInternalError,
					Reason: fmt.Sprintf("failed to write to process stdin: %s", err),
				}
				return
			}
			logger.Debug("wrote stdin from websocket to process",
				slog.Int("bytes", n),
			)
		case websocket.MessageText:
			// Handle OoB message
			oobMsg, err := protocol.ReadWebSocketOoBMessage(msg)
			if err != nil {
				logger.Error("failed to extract OoB message payload", slog.Any("error", err))
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusUnsupportedData,
					Reason: fmt.Sprintf("failed to extract OoB message payload: %s", err),
				}
				return
			}
			switch typedMsg := oobMsg.(type) {
			case protocol.OoBMessageShutdownPayload:
				logger.Info("shutdown message received, closing stdin")
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusNormalClosure,
					Reason: "shutdown message acknowledged",
				}
				return
			default:
				logger.Warn("unknown OoB message",
					slog.Any("payload", typedMsg),
				)
				// continue
			}
		default:
			// should never happen
			logger.Error("unexpected websocket message type", slog.Any("type", msgType))
			returnErr <- &websocket.CloseError{
				Code:   websocket.StatusUnsupportedData,
				Reason: "unexpected websocket message type",
			}
			return
		}
	}
}

func senderStdout(ctx context.Context, wsc *websocket.Conn, processStdout *io.PipeReader, logger *slog.Logger, returnErr chan<- *websocket.CloseError) {
	// Avoid blocking on processStdout.Read() when context is cancelled
	go func() {
		<-ctx.Done()
		processStdout.Close()
	}()
	defer processStdout.Close()
	var (
		buf       = make([]byte, protocol.BinaryFrameMaxSize)
		n         int
		err, werr error
	)
	for {
		n, err = processStdout.Read(buf)
		if n > 0 {
			if werr = wsc.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				logger.Error("failed to write stdout to websocket", slog.Any("error", werr))
				if websocket.CloseStatus(werr) == -1 {
					returnErr <- &websocket.CloseError{
						Code:   websocket.StatusInternalError,
						Reason: fmt.Sprintf("failed to write stdout to websocket: %s", werr),
					}
				} else {
					// websocket is close, do not bother closing it properly
					returnErr <- nil
				}
				return
			}
			logger.Debug("sent stdout read to websocket",
				slog.Int("bytes", n),
			)
		}
		if err != nil {
			// Note on EOF vs io.ErrClosedPipe:
			// - io.EOF: occurs when the process closes stdout (write end) and we read
			//   all remaining data before the read end is closed. This is rare in
			//   normal shutdown because we cancel the context (closing the read end)
			//   immediately after Wait() returns.
			// - io.ErrClosedPipe: occurs when the read end is closed (by our context
			//   cancellation helper) while Read() is blocked. This is the common
			//   case during normal shutdown: process exits → Wait() returns → we
			//   cancel context → read end closes → Read() returns ErrClosedPipe.
			if err == io.EOF {
				logger.Debug("stdout EOF received")
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusNormalClosure,
					Reason: "stdout EOF",
				}
			} else if errors.Is(err, io.ErrClosedPipe) {
				// Expected during normal shutdown - read end closed by context cancellation
				logger.Debug("stdout pipe closed (expected during shutdown)")
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusNormalClosure,
					Reason: "stdout pipe closed",
				}
			} else {
				logger.Error("failed to read stdout", slog.Any("error", err))
				returnErr <- &websocket.CloseError{
					Code:   websocket.StatusInternalError,
					Reason: fmt.Sprintf("stdout read error: %s", err),
				}
			}
			return
		}
	}
}

func senderStderr(ctx context.Context, wsc *websocket.Conn, processStderr *io.PipeReader, logger *slog.Logger, returnErr chan<- struct{}) {
	// Avoid blocking on processStderr.Read() when context is cancelled
	go func() {
		<-ctx.Done()
		processStderr.Close()
	}()
	defer func() {
		returnErr <- struct{}{} // Signal exit but don't trigger shutdown (stderr is optional)
		processStderr.Close()
	}()
	var (
		line, logLine string
		err, sendErr  error
	)
	// Use bufio.Reader instead of Scanner for resilience against long lines
	stderrReader := bufio.NewReader(processStderr)
	for {
		line, err = stderrReader.ReadString('\n')
		// Process any data we got (even if err != nil)
		line = strings.TrimSuffix(line, "\n")
		if len(line) > 0 {
			if len(line) > 1024 {
				logLine = line[:1024] + "..."
			} else {
				logLine = line
			}
			logger.Info("process emitted a stderr line",
				slog.String("line", logLine),
			)
			sendErr = protocol.OoBMessageProcessStderrPayload{
				Line: line,
			}.SendWebSocketOoBMessage(ctx, wsc)
			if sendErr != nil {
				logger.Error("failed to send stderr line to client",
					slog.Any("error", sendErr),
					slog.String("line", line),
				)
				// WebSocket dead, but don't exit - continue reading to avoid blocking process
			}
		}
		// Handle read errors - stderr is optional, so just exit on any error
		if err != nil {
			if err == io.EOF {
				logger.Debug("stderr EOF received")
			} else if ctx.Err() != nil {
				logger.Debug("stderr reader stopped due to context cancellation")
			} else {
				logger.Error("failed to read stderr, stopping stderr forwarding", slog.Any("error", err))
			}
			return
		}
	}
}
