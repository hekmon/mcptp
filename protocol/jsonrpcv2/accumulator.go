package jsonrpcv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type Accumulator struct {
	commandBuffer bytes.Buffer
}

// AccumulateRequest buffers incoming data and returns a complete JSON-RPC request when available.
// MCP messages are line-delimited, so data is accumulated until a complete line is received.
// Any additional complete lines in the buffer are discarded to maintain protocol synchronization.
//
// NOTE: Use either AccumulateRequest OR AccumulateResponse on a given Accumulator instance,
// never both. Mixing requests and responses in the same buffer would cause protocol
// desynchronization since they are different message types.
func (ra *Accumulator) AccumulateRequest(data []byte) (request *Request, err error) {
	// Read a complete line from the buffer (common logic)
	commandLine, err := ra.readCompleteLine(data)
	if err != nil || commandLine == nil {
		return
	}
	// Try to parse the (1st) command line we've read
	request = new(Request)
	if err = json.Unmarshal(commandLine, request); err != nil {
		err = fmt.Errorf("failed to unmarshal command line: %w", err)
		return
	}
	if !request.Valid() {
		err = errors.New("invalid request")
		return
	}
	return
}

// AccumulateResponse buffers incoming data and returns a complete JSON-RPC response when available.
// MCP messages are line-delimited, so data is accumulated until a complete line is received.
// Any additional complete lines in the buffer are discarded to maintain protocol synchronization.
//
// NOTE: Use either AccumulateRequest OR AccumulateResponse on a given Accumulator instance,
// never both. Mixing requests and responses in the same buffer would cause protocol
// desynchronization since they are different message types.
func (ra *Accumulator) AccumulateResponse(data []byte) (response *Response, err error) {
	// Read a complete line from the buffer (common logic)
	commandLine, err := ra.readCompleteLine(data)
	if err != nil || commandLine == nil {
		return
	}
	// Try to parse the (1st) command line we've read
	response = new(Response)
	if err = json.Unmarshal(commandLine, response); err != nil {
		err = fmt.Errorf("failed to unmarshal command line: %w", err)
		return
	}
	if !response.Valid() {
		err = errors.New("invalid response")
		return
	}
	return
}

// readCompleteLine buffers incoming data and returns a complete line (including the newline).
// Returns nil, nil if no complete line is available yet (accumulate more data).
// Returns nil, err if a read error or corruption occurred (buffer reset).
// Drains any additional complete lines to maintain protocol synchronization.
func (ra *Accumulator) readCompleteLine(data []byte) (commandLine []byte, err error) {
	// Accumulate incoming data into the buffer.
	// MCP messages are line-delimited, so we buffer until we have a complete line.
	if _, err = ra.commandBuffer.Write(data); err != nil {
		err = fmt.Errorf("failed to write data to buffer: %w", err)
		return
	}
	// Try to read a command line
	idx := bytes.IndexByte(ra.commandBuffer.Bytes(), '\n')
	if idx == -1 {
		// line is not complete yet
		return
	}
	commandLine = make([]byte, idx+1)
	var n int
	if n, err = ra.commandBuffer.Read(commandLine); err != nil {
		err = fmt.Errorf("failed to read command line: %w", err)
		return
	}
	if n != idx+1 {
		err = fmt.Errorf("failed to read command line: expected %d bytes, got %d", idx+1, n)
		ra.commandBuffer.Reset() // Clear corrupted/inconsistent state
		return
	}
	// Drain any remaining complete lines from the buffer to stay synchronized.
	for {
		// MCP is a strict request/response protocol: we must process exactly one message
		// per call and stay synchronized with the peer's turn-taking expectations.
		// This drain happens regardless of whether the current line parses successfully:
		// we've already identified message boundaries, so discarding extras prevents "drift"
		// where we'd process stale buffered messages on subsequent calls while the peer
		// has already moved to the next exchange.
		if idx = bytes.IndexByte(ra.commandBuffer.Bytes(), '\n'); idx == -1 {
			break
		}
		_ = ra.commandBuffer.Next(idx + 1)
	}
	return
}
