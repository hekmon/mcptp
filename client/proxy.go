package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hekmon/mcptp/protocol"

	"github.com/coder/websocket"
	"github.com/urfave/cli/v3"
)

func proxy(ctx context.Context, conn *websocket.Conn) (err error) {
	// Wait for start_ok from server
	msgType, msg, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("failed to read from websocket: %w", err)
	}
	if msgType != websocket.MessageText {
		return errors.New("expected start status as text message from server, got a binary message")
	}
	var oobMsg protocol.OoBPayload
	if oobMsg, err = protocol.ReadWebSocketOoBMessage(msg); err != nil {
		return fmt.Errorf("failed to parse OoB message: %w", err)
	}
	switch typedMsg := oobMsg.(type) {
	case protocol.OoBMessageStartOKPayload:
		fmt.Fprintf(os.Stderr, "remote server started (PID %d)\n", typedMsg.PID)
		// Expected, continue
	case protocol.OoBMessageStartErrPayload:
		return fmt.Errorf("server failed to start process: %s", typedMsg.Error)
	default:
		return fmt.Errorf("unexpected OoB message type: %T", typedMsg)
	}

	// Start the IO workers
	receiverStdinChan := make(chan cli.ExitCoder, 1)
	receiverStdinContext, receiverStdinContextCancel := context.WithCancel(ctx)
	defer receiverStdinContextCancel()
	go receiverStdin(receiverStdinContext, conn, os.Stdin, receiverStdinChan)
	senderStdoutChan := make(chan cli.ExitCoder, 1)
	senderStdoutContext, senderStdoutContextCancel := context.WithCancel(ctx)
	defer senderStdoutContextCancel()
	go senderStdout(senderStdoutContext, conn, os.Stdout, senderStdoutChan)

	// Wait for either worker to finish
	var exitErr cli.ExitCoder
	select {
	case exitErr = <-receiverStdinChan:
		// Receiver finished first: cancel sender and wait for it
		senderStdoutContextCancel()
		<-senderStdoutChan
	case exitErr = <-senderStdoutChan:
		// Sender finished first (e.g., Ctrl+C pressed, websocket closed).
		// Cancel receiver context and wait for it to finish.
		//
		// IMPORTANT: On Windows/WSL terminals, stdin.Read() is not interruptible.
		// Even after context cancellation, the receiver goroutine may remain
		// blocked on stdin.Read() indefinitely. This is a platform limitation:
		// console handles on Windows do not support async cancellation.
		//
		// We use a timeout to avoid hanging forever. If the receiver doesn't
		// finish within 100ms, we exit anyway. The stuck goroutine will be
		// cleaned up when the process exits - this is safe because:
		// 1. The process is terminating anyway
		// 2. The goroutine holds no external resources (just stack memory)
		// 3. The OS reclaims all process resources on exit
		receiverStdinContextCancel()
		select {
		case <-receiverStdinChan:
			// Receiver finished in time (normal case on Unix, or when not blocked on read)
		case <-time.After(100 * time.Millisecond):
			// Receiver didn't finish - it's stuck on stdin.Read().
			// Exit anyway; the goroutine will be cleaned up by the OS.
		}
	}
	return exitErr
}

// receiver reads from stdin and writes to websocket.
// Sends shutdown OoB message when stdin closes.
func receiverStdin(ctx context.Context, conn *websocket.Conn, stdin *os.File, returnErr chan<- cli.ExitCoder) {
	var (
		buf     = make([]byte, protocol.BinaryFrameMaxSize)
		n       int
		err     error
		sendErr error
	)
	for {
		n, err = stdin.Read(buf)
		if n > 0 {
			if sendErr = conn.Write(ctx, websocket.MessageBinary, buf[:n]); sendErr != nil {
				returnErr <- cli.Exit(fmt.Errorf("failed to write stdin to websocket: %w", sendErr), 1)
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				sendErr = protocol.OoBMessageShutdownPayload{}.SendWebSocketOoBMessage(ctx, conn)
				if sendErr != nil {
					returnErr <- cli.Exit(fmt.Errorf("failed to send shutdown message: %w", sendErr), 1)
					return
				}
				// Wait for sender to finish (it will receive server_exited or connection close)
				<-ctx.Done()
				returnErr <- cli.Exit(ctx.Err(), 1) // still returning an error just in case (but in that path, it should be discarded)
				return
			}
			returnErr <- cli.Exit(fmt.Errorf("failed to read stdin: %w", err), 1)
			return
		}
	}
}

// sender reads from websocket and writes to stdout.
// Handles OoB messages (stderr, server_exited).
func senderStdout(ctx context.Context, conn *websocket.Conn, stdout io.Writer, returnErr chan<- cli.ExitCoder) {
	var (
		msgType  websocket.MessageType
		msg      []byte
		err      error
		exitCode int
		exitInfo string
	)
	conn.SetReadLimit(protocol.BinaryFrameMaxSize)
	for {
		if msgType, msg, err = conn.Read(ctx); err != nil {
			if ce, ok := errors.AsType[websocket.CloseError](err); ok {
				if ce.Code == websocket.StatusNormalClosure {
					returnErr <- cli.Exit(fmt.Errorf("websocket closed with status %d (%s): %s", ce.Code, ce.Reason, exitInfo), exitCode)
				} else {
					returnErr <- cli.Exit(fmt.Errorf("websocket unexpectedly closed with status %d (%s): %s", ce.Code, ce.Reason, exitInfo), 1)
				}
				return
			}
			returnErr <- cli.Exit(fmt.Errorf("websocket read error: %v", err), 1)
			return
		}
		switch msgType {
		case websocket.MessageBinary:
			if _, err = stdout.Write(msg); err != nil {
				returnErr <- cli.Exit(fmt.Errorf("failed to write to stdout: %w", err), 1)
				return
			}
		case websocket.MessageText:
			// Handle OoB message
			oobMsg, parseErr := protocol.ReadWebSocketOoBMessage(msg)
			if parseErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to parse OoB message: %v\n", parseErr)
				continue
			}
			switch typedMsg := oobMsg.(type) {
			case protocol.OoBMessageProcessStderrPayload:
				// Forward stderr line to local stderr
				fmt.Fprintf(os.Stderr, "[SERVER] %s\n", typedMsg.Line)
			case protocol.OoBMessageServerExitedPayload:
				// Server process exited: forward to local stderr and close
				if typedMsg.ExitCode != nil {
					exitInfo = fmt.Sprintf("exit code %d", *typedMsg.ExitCode)
					exitCode = *typedMsg.ExitCode
				} else if typedMsg.Killed {
					exitInfo = "killed"
					exitCode = 1
				} else {
					exitInfo = "error"
					exitCode = 1
				}
				if typedMsg.Error != "" {
					exitInfo = fmt.Sprintf("process exited (%s): %s", exitInfo, typedMsg.Error)
				} else {
					exitInfo = fmt.Sprintf("process exited (%s)", exitInfo)
				}
				// continue to catch the server closure that will follow this message
			case protocol.OoBMessageShutdownPayload:
				// Should never happen - shutdown is client-to-server only
				fmt.Fprintf(os.Stderr, "warning: protocol violation - received shutdown message from server\n")
			default:
				// Ignore other OoB messages (start_ok already handled)
			}
		default:
			fmt.Fprintf(os.Stderr, "warning: unexpected websocket message type: %v\n", msgType)
		}
	}
}
