package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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
		// Sender finished first: cancel receiver and wait for it
		receiverStdinContextCancel()
		<-receiverStdinChan
	}
	return exitErr
}

// receiver reads from stdin and writes to websocket.
// Sends shutdown OoB message when stdin closes.
func receiverStdin(ctx context.Context, conn *websocket.Conn, stdin *os.File, returnErr chan<- cli.ExitCoder) {
	go func() {
		// Avoid blocking on stdin.Read() when context is cancelled
		<-ctx.Done()
		stdin.Close()
	}()
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
				// do not return just yet to avoid triggering an error based stop
				// instead wait for the sender to finish and cancel us
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
		msgType websocket.MessageType
		msg     []byte
		err     error
	)
	conn.SetReadLimit(protocol.BinaryFrameMaxSize)
	for {
		if msgType, msg, err = conn.Read(ctx); err != nil {
			if ce, ok := errors.AsType[websocket.CloseError](err); ok {
				returnErr <- cli.Exit(fmt.Errorf("websocket unexpectedly closed with status %d (%s)", ce.Code, ce.Reason), 1)
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
				var (
					exitInfo string
					exitCode int
				)
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
				returnErr <- cli.Exit(exitInfo, exitCode)
				return
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
