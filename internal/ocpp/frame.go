// Package ocpp implements the OCPP-J 1.6 wire format: the RPC framing
// (CALL / CALLRESULT / CALLERROR as JSON arrays) and the typed payloads for
// the v1 message set. Field names and enums follow the official OCPP 1.6
// JSON schemas — when the spec and convenience disagree, the spec wins.
package ocpp

import (
	"encoding/json"
	"fmt"
)

// MessageType is the first element of every OCPP-J frame.
type MessageType int

const (
	Call       MessageType = 2
	CallResult MessageType = 3
	CallError  MessageType = 4
)

// Frame is a decoded OCPP-J message. Exactly one of Payload (CALL,
// CALLRESULT) or the error fields (CALLERROR) is meaningful.
type Frame struct {
	Type      MessageType
	MessageID string
	// Action is present on CALL only.
	Action string
	// Payload is the raw JSON payload, decoded later against the typed
	// structs — the frame layer does not know message semantics.
	Payload json.RawMessage
	// CALLERROR fields.
	ErrorCode        string
	ErrorDescription string
	ErrorDetails     json.RawMessage
}

// Decode parses a raw OCPP-J array into a Frame.
func Decode(raw []byte) (Frame, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return Frame{}, fmt.Errorf("ocpp-j frame is not a JSON array: %w", err)
	}
	if len(parts) < 3 {
		return Frame{}, fmt.Errorf("ocpp-j frame has %d elements, want >= 3", len(parts))
	}
	var f Frame
	var t int
	if err := json.Unmarshal(parts[0], &t); err != nil {
		return Frame{}, fmt.Errorf("message type: %w", err)
	}
	f.Type = MessageType(t)
	if err := json.Unmarshal(parts[1], &f.MessageID); err != nil {
		return Frame{}, fmt.Errorf("message id: %w", err)
	}
	switch f.Type {
	case Call:
		if len(parts) != 4 {
			return Frame{}, fmt.Errorf("CALL has %d elements, want 4", len(parts))
		}
		if err := json.Unmarshal(parts[2], &f.Action); err != nil {
			return Frame{}, fmt.Errorf("action: %w", err)
		}
		f.Payload = parts[3]
	case CallResult:
		if len(parts) != 3 {
			return Frame{}, fmt.Errorf("CALLRESULT has %d elements, want 3", len(parts))
		}
		f.Payload = parts[2]
	case CallError:
		if len(parts) != 5 {
			return Frame{}, fmt.Errorf("CALLERROR has %d elements, want 5", len(parts))
		}
		if err := json.Unmarshal(parts[2], &f.ErrorCode); err != nil {
			return Frame{}, fmt.Errorf("error code: %w", err)
		}
		if err := json.Unmarshal(parts[3], &f.ErrorDescription); err != nil {
			return Frame{}, fmt.Errorf("error description: %w", err)
		}
		f.ErrorDetails = parts[4]
	default:
		return Frame{}, fmt.Errorf("unknown message type %d", t)
	}
	return f, nil
}

// EncodeCall builds a CALL frame.
func EncodeCall(messageID, action string, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", action, err)
	}
	return json.Marshal([]any{int(Call), messageID, action, json.RawMessage(p)})
}

// EncodeCallResult builds a CALLRESULT frame.
func EncodeCallResult(messageID string, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal call result: %w", err)
	}
	return json.Marshal([]any{int(CallResult), messageID, json.RawMessage(p)})
}

// EncodeCallError builds a CALLERROR frame. Error codes are the spec's
// (NotImplemented, NotSupported, InternalError, ...).
func EncodeCallError(messageID, code, description string) ([]byte, error) {
	return json.Marshal([]any{int(CallError), messageID, code, description, json.RawMessage(`{}`)})
}
