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
		// TODO: send error to client
		return
	}
	// Start the IO workers
	var (
		workers                sync.WaitGroup
		receiverErr, senderErr error
	)
	receiverDone := make(chan struct{})
	workers.Add(1)
	go func() {
		receiverErr = receiver(processCtx, wsc, subProcessStdinWriter, logger)
		close(receiverDone)
	}()
	senderDone := make(chan struct{})
	workers.Add(1)
	go func() {
		senderErr = sender(processCtx, wsc, subProcessStdoutReader, subProcessStderrReader, logger)
		close(senderDone)
	}()
	// Wait for either the receiver or the sender to finish to act according to the error they returned
	select {
	case <-receiverDone:
		// Act acordingly depending on receiverErr
		// TODO
	case <-senderDone:
		// Act acordingly depending on senderErr
		// TODO
	}
	// Wait for the process to finish with a timeout
	subProcess.WaitDelay = protocol.SubProcessGracePeriod
	if err := subProcess.Wait(); err != nil {
		logger.Error("process failed", slog.Any("error", err))
	}
	// Wait for the workers to properly finish
	workers.Wait()
	wsc.Close(websocket.StatusNormalClosure, "")
}

func receiver(ctx context.Context, wsc *websocket.Conn, processStdin io.WriteCloser, logger *slog.Logger) (err error) {
	defer processStdin.Close()
	// Start the read/write loop
	var (
		msgType websocket.MessageType
		msg     []byte
	)
	wsc.SetReadLimit(-1) // disable read limit
	for {
		// Read a websocket message
		if msgType, msg, err = wsc.Read(ctx); err != nil {
			statusCode := websocket.CloseStatus(err)
			switch statusCode {
			case -1:
				logger.Error("failed to read websocket message", slog.Any("error", err))
				return err
			case websocket.StatusNormalClosure:
				logger.Warn("websocket connection closed without OoB shutdown received")
				// TODO initiate process shutdown
				return nil
			default:
				// Websocket closed
				logger.Warn("websocket connection closed without OoB shutdown received",
					slog.Int("status_code", int(statusCode)),
					slog.String("status_text", statusCode.String()),
				)
				return nil // TODO: return a normal stop
			}
		}
		// Handle the message
		switch msgType {
		case websocket.MessageBinary:
			if _, err = processStdin.Write(msg); err != nil {
				// TODO: handle error
				logger.Error("failed to write to process stdin", slog.Any("error", err))
				return
			}
			// TODO log JSON-RPC message
		case websocket.MessageText:
			// Unmarshal OoB message
			var oobMessage protocol.OoBMessage
			if err = json.Unmarshal(msg, &oobMessage); err != nil {
				logger.Error("failed to unmarshal OOB message", slog.Any("error", err))
				return
			}
			logger.Info("received oob message", slog.String("type", string(oobMessage.Type)))
			// Handle OOB message types
			switch oobMessage.Type {
			case protocol.OoBMessageShutdown:
			// TODO: handle shutdown
			default:
				if oobMessage.Type == "" {
					logger.Error("unexpected text message received",
						slog.String("msg", string(msg)),
					)
					return errors.New("unexpected text message received")
				}
				logger.Warn("unknown OOB message type",
					slog.String("type", string(oobMessage.Type)),
					slog.String("payload", string(oobMessage.Payload)),
				)
			}
		default:
			logger.Error("unexpected message type", slog.Any("type", msgType))
			return errors.New("unexpected message type")
		}
	}
}

func sender(ctx context.Context, wsc *websocket.Conn, processStdout, processStderr io.Reader, logger *slog.Logger) (err error) {
	// TODO
	return nil
}
