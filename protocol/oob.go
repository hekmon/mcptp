package protocol

import (
	"encoding/json"
	"fmt"
)

type OoBMessage struct {
	Type    OoBMessageType  `json:"command"`
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
	default:
		err = fmt.Errorf("unknown message type: %s", m.Type)
		return
	}
}

type OoBPayload interface {
	WebSocketMessage() (jsonMessage []byte, err error)
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

func (m OoBMessageStartOKPayload) WebSocketMessage() (jsonMessage []byte, err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshall payload: %w", err)
		return
	}
	if jsonMessage, err = json.Marshal(OoBMessage{
		Type:    OoBMessageStartOK,
		Payload: payload,
	}); err != nil {
		err = fmt.Errorf("failed to marshall envelope: %w", err)
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

func (m OoBMessageStartErrPayload) WebSocketMessage() (jsonMessage []byte, err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshall payload: %w", err)
		return
	}
	if jsonMessage, err = json.Marshal(OoBMessage{
		Type:    OoBMessageStartErr,
		Payload: payload,
	}); err != nil {
		err = fmt.Errorf("failed to marshall envelope: %w", err)
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

func (m OoBMessageShutdownPayload) WebSocketMessage() (jsonMessage []byte, err error) {
	payload, err := json.Marshal(m)
	if err != nil {
		err = fmt.Errorf("failed to marshall payload: %w", err)
		return
	}
	if jsonMessage, err = json.Marshal(OoBMessage{
		Type:    OoBMessageShutdown,
		Payload: payload,
	}); err != nil {
		err = fmt.Errorf("failed to marshall envelope: %w", err)
		return
	}
	return
}
