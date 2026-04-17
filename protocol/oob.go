package protocol

import "encoding/json"

type OoBMessageType string

const (
	OoBMessageShutdown OoBMessageType = "shutdown" // sent by client when its parent closed stdin: signal for MCP to stop
)

type OoBMessage struct {
	Type    OoBMessageType  `json:"command"`
	Payload json.RawMessage `json:"data,omitempty"`
}
