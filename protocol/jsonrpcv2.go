package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONRPCVersion is the version string for JSON-RPC 2.0
const JSONRPCVersion = "2.0"

// ReservedMethodPrefix is the prefix reserved for RPC-internal methods and extensions.
// Method names beginning with this prefix MUST NOT be used for anything else.
const ReservedMethodPrefix = "rpc."

// Request represents a JSON-RPC 2.0 request object.
// See https://www.jsonrpc.org/specification#request_object
type Request struct {
	// JSONRPC specifies the version of the JSON-RPC protocol. Must be "2.0".
	JSONRPC string `json:"jsonrpc"`
	// Method contains the name of the method to be invoked.
	Method string `json:"method"`
	// Params holds the parameter values to be used during the invocation.
	// This may be an Array (by-position) or an Object (by-name), or omitted.
	Params json.RawMessage `json:"params,omitempty"`
	// ID is an identifier established by the Client.
	// If omitted, the request is a notification and no response is expected.
	// Can be a string, number, or null.
	ID any `json:"id,omitempty"`
}

// Valid checks if the Request is a valid JSON-RPC 2.0 request.
// It validates that the jsonrpc field is "2.0" and the method is not empty.
func (r Request) Valid() bool {
	return r.JSONRPC == JSONRPCVersion && r.Method != ""
}

// IsNotification returns true if the request is a notification (no ID).
func (r Request) IsNotification() bool {
	return r.ID == nil
}

// IsReservedMethod returns true if the method name is reserved for RPC-internal methods.
// Method names beginning with "rpc." are reserved per the JSON-RPC 2.0 specification.
func (r Request) IsReservedMethod() bool {
	return strings.HasPrefix(r.Method, ReservedMethodPrefix)
}

// Response represents a JSON-RPC 2.0 response object.
// See https://www.jsonrpc.org/specification#response_object
type Response struct {
	// JSONRPC specifies the version of the JSON-RPC protocol. Must be "2.0".
	JSONRPC string `json:"jsonrpc"`
	// Result contains the result of the method invocation on success.
	// This must not exist if there was an error.
	Result json.RawMessage `json:"result,omitempty"`
	// Error contains error information if the method invocation failed.
	// This must not exist if there was no error.
	Error *Error `json:"error,omitempty"`
	// ID matches the id from the corresponding Request object.
	// This field is always present in responses.
	// It can be a string, number, or null (null when the request ID couldn't be detected).
	ID any `json:"id"`
}

// Valid checks if the Response is a valid JSON-RPC 2.0 response.
// It validates that the jsonrpc field is "2.0", ID is present, and either Result or Error exists (not both).
func (r Response) Valid() bool {
	if r.JSONRPC != JSONRPCVersion {
		return false
	}
	// ID must be present (can be null, but not omitted)
	// HasResult and HasError are mutually exclusive
	hasResult := len(r.Result) > 0
	hasError := r.Error != nil
	return hasResult != hasError // XOR: exactly one must be true
}

// IsSuccess returns true if the response contains a result (no error).
func (r Response) IsSuccess() bool {
	return len(r.Result) > 0 && r.Error == nil
}

// IsError returns true if the response contains an error.
func (r Response) IsError() bool {
	return r.Error != nil
}

// Error represents a JSON-RPC 2.0 error object.
// See https://www.jsonrpc.org/specification#error_object
type Error struct {
	// Code indicates the error type that occurred.
	Code ErrorCode `json:"code"`
	// Message provides a short description of the error.
	Message string `json:"message"`
	// Data contains additional information about the error (optional).
	Data json.RawMessage `json:"data,omitempty"`
}

// Valid checks if the Error is a valid JSON-RPC 2.0 error.
// It validates that the code is set and message is not empty.
func (e Error) Valid() bool {
	return e.Code != 0 && e.Message != ""
}

// ErrorCode represents a JSON-RPC error code.
// See https://www.jsonrpc.org/specification#error_object
type ErrorCode int

// String returns the standard message for pre-defined error codes.
func (e ErrorCode) String() string {
	switch e {
	case ErrorCodeParseError:
		return "Parse error"
	case ErrorCodeInvalidRequest:
		return "Invalid Request"
	case ErrorCodeMethodNotFound:
		return "Method not found"
	case ErrorCodeInvalidParams:
		return "Invalid params"
	case ErrorCodeInternalError:
		return "Internal error"
	default:
		if e >= ErrorCodeServerErrorMin && e <= ErrorCodeServerErrorMax {
			return fmt.Sprintf("Server error (%d)", e)
		}
		return fmt.Sprintf("Error code %d", e)
	}
}

// Pre-defined error codes as per JSON-RPC 2.0 specification
const (
	ErrorCodeParseError     ErrorCode = -32700 // Invalid JSON was received
	ErrorCodeInvalidRequest ErrorCode = -32600 // The JSON sent is not a valid Request object
	ErrorCodeMethodNotFound ErrorCode = -32601 // The method does not exist / is not available
	ErrorCodeInvalidParams  ErrorCode = -32602 // Invalid method parameter(s)
	ErrorCodeInternalError  ErrorCode = -32603 // Internal JSON-RPC error
	ErrorCodeServerErrorMin ErrorCode = -32000 // Reserved for implementation-defined server-errors
	ErrorCodeServerErrorMax ErrorCode = -32099
)

// Batch represents a JSON-RPC 2.0 batch request.
// A batch is an array of Request objects.
// See https://www.jsonrpc.org/specification#batch
type Batch []Request

// Valid checks if the Batch is a valid JSON-RPC 2.0 batch request.
// It validates that the batch is not empty and each Request is valid.
func (b Batch) Valid() bool {
	if len(b) == 0 {
		return false
	}
	for _, req := range b {
		if !req.Valid() {
			return false
		}
	}
	return true
}

// BatchResponse represents a JSON-RPC 2.0 batch response.
// A batch response is an array of Response objects.
// See https://www.jsonrpc.org/specification#batch
type BatchResponse []Response

// Valid checks if the BatchResponse is a valid JSON-RPC 2.0 batch response.
// It validates that each Response is valid.
func (br BatchResponse) Valid() bool {
	for _, resp := range br {
		if !resp.Valid() {
			return false
		}
	}
	return true
}
