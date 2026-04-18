package protocol

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
)

type OoBMessage struct {
	Type    OoBMessageType  `json:"type"`
	Payload json.RawMessage `json:"data,omitempty"`
}

type OoBMessageType string

func (m OoBMessage) WebSocketMessagePayload() (payload OoBPayload, err error) {
	switch m.Type {
	case OoBMessageStartOK:
		var received OoBMessageStartOKPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case OoBMessageStartErr:
		var received OoBMessageStartErrPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case OoBMessageShutdown:
		var received OoBMessageShutdownPayload
		if err = json.Unmarshal(m.Payload, &received); err != nil {
			err = fmt.Errorf("failed to unmarshal payload: %w", err)
			return
		}
		payload = received
		return
	case OoBMessageServerExited:
		var received OoBMessageServerExitedPayload
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

type OoBPayload interface {
	SendWebSocketMessage(ctx context.Context, wsc *websocket.Conn) (err error)
}

/*
 * Start OK
 */

const (
	OoBMessageStartOK OoBMessageType = "start_ok" // sent by MCP when it's ready to receive commands
)

type OoBMessageStartOKPayload struct {
	PID int `json:"pid"`
}

func (m OoBMessageStartOKPayload) SendWebSocketMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(OoBMessage{
		Type:    OoBMessageStartOK,
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
	OoBMessageStartErr OoBMessageType = "start_err" // sent by MCP when it failed to start
)

type OoBMessageStartErrPayload struct {
	Error string `json:"error"`
}

func (m OoBMessageStartErrPayload) SendWebSocketMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(OoBMessage{
		Type:    OoBMessageStartErr,
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
	OoBMessageShutdown OoBMessageType = "shutdown" // sent by client when its parent closed stdin: signal for MCP to stop
)

type OoBMessageShutdownPayload struct{}

func (m OoBMessageShutdownPayload) SendWebSocketMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(OoBMessage{
		Type:    OoBMessageShutdown,
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
	OoBMessageServerExited OoBMessageType = "server_exited" // sent by server when MCP process exits
)

type OoBMessageServerExitedPayload struct {
	ExitCode *int   `json:"exit_code"` // Present if exited normally, absent if killed or other errors
	Killed   bool   `json:"killed"`
	Error    string `json:"error"` // Present if error occurred
}

func (m OoBMessageServerExitedPayload) SendWebSocketMessage(ctx context.Context, wsc *websocket.Conn) (err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshal payload: %w", err)
		return
	}
	jsonMessage, err := json.Marshal(OoBMessage{
		Type:    OoBMessageServerExited,
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
