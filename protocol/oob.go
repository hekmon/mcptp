package protocol

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
)

type OoBPayload interface {
	SendWebSocketOoBMessage(ctx context.Context, wsc *websocket.Conn) (err error)
}

func ReadWebSocketOoBMessage(textMsg []byte) (payload OoBPayload, err error) {
	var m oobMessage
	if err = json.Unmarshal(textMsg, &m); err != nil {
		err = fmt.Errorf("failed to unmarshal message: %w", err)
		return
	}
	switch m.Type {
	case oobMessageStartOK:
		var received OoBMessageStartOKPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case oobMessageStartErr:
		var received OoBMessageStartErrPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case oobMessageShutdown:
		var received OoBMessageShutdownPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case oobMessageServerExited:
		var received OoBMessageServerExitedPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case oobMessageProcessStderr:
		var received OoBMessageProcessStderrPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	default:
		err = fmt.Errorf("unknown message type: %s", m.Type)
		return
	}
}

type oobMessage struct {
	Type    oobMessageType  `json:"type"`
	Payload json.RawMessage `json:"data,omitempty"`
}

type oobMessageType string

/*
 * Start OK
 */

const (
	oobMessageStartOK oobMessageType = "start_ok" // sent by MCP when it's ready to receive commands
)

type OoBMessageStartOKPayload struct {
	PID int `json:"pid"`
}

func (m OoBMessageStartOKPayload) SendWebSocketOoBMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(oobMessage{
		Type:    oobMessageStartOK,
		Payload: payload,
	})
	if err != nil {
		err = fmt.Errorf("failed to marshal envelope: %w", err)
		return
	}
	if err = wsc.Write(ctx, websocket.MessageText, jsonMessage); err != nil {
		err = fmt.Errorf("failed to send message: %w", err)
		return
	}
	return
}

/*
 * Start Error
 */

const (
	oobMessageStartErr oobMessageType = "start_err" // sent by MCP when it failed to start
)

type OoBMessageStartErrPayload struct {
	Error string `json:"error"`
}

func (m OoBMessageStartErrPayload) SendWebSocketOoBMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(oobMessage{
		Type:    oobMessageStartErr,
		Payload: payload,
	})
	if err != nil {
		err = fmt.Errorf("failed to marshal envelope: %w", err)
		return
	}
	if err = wsc.Write(ctx, websocket.MessageText, jsonMessage); err != nil {
		err = fmt.Errorf("failed to send message: %w", err)
		return
	}
	return
}

/*
 * Shutdown
 */

const (
	oobMessageShutdown oobMessageType = "shutdown" // sent by client when its parent closed stdin: signal for MCP to stop
)

type OoBMessageShutdownPayload struct{}

func (m OoBMessageShutdownPayload) SendWebSocketOoBMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(oobMessage{
		Type:    oobMessageShutdown,
		Payload: payload,
	})
	if err != nil {
		err = fmt.Errorf("failed to marshal envelope: %w", err)
		return
	}
	if err = wsc.Write(ctx, websocket.MessageText, jsonMessage); err != nil {
		err = fmt.Errorf("failed to send message: %w", err)
		return
	}
	return
}

/*
 * Server Exited
 */

const (
	oobMessageServerExited oobMessageType = "server_exited" // sent by server when MCP process exits
)

type OoBMessageServerExitedPayload struct {
	ExitCode *int   `json:"exit_code"` // Present if exited normally, absent if killed or other errors
	Killed   bool   `json:"killed"`
	Error    string `json:"error"` // Present if error occurred
}

func (m OoBMessageServerExitedPayload) SendWebSocketOoBMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(oobMessage{
		Type:    oobMessageServerExited,
		Payload: payload,
	})
	if err != nil {
		err = fmt.Errorf("failed to marshal envelope: %w", err)
		return
	}
	if err = wsc.Write(ctx, websocket.MessageText, jsonMessage); err != nil {
		err = fmt.Errorf("failed to send message: %w", err)
		return
	}
	return
}

/*
 * Process stderr
 */

const (
	oobMessageProcessStderr oobMessageType = "process_stderr"
)

type OoBMessageProcessStderrPayload struct {
	Line string `json:"line"` // the log line
}

func (m OoBMessageProcessStderrPayload) SendWebSocketOoBMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(oobMessage{
		Type:    oobMessageProcessStderr,
		Payload: payload,
	})
	if err != nil {
		err = fmt.Errorf("failed to marshal envelope: %w", err)
		return
	}
	if err = wsc.Write(ctx, websocket.MessageText, jsonMessage); err != nil {
		err = fmt.Errorf("failed to send message: %w", err)
		return
	}
	return
}
