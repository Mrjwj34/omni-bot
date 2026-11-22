package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const JSONRPCVersion = "2.0"

const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603

	ErrCodeNotRegistered   = -32001
	ErrCodeChannelNotFound = -32002
	ErrCodeDeliveryFailed  = -32003
)

var (
	ErrInvalidJSON    = errors.New("invalid json-rpc payload")
	ErrInvalidVersion = errors.New("unsupported jsonrpc version")
)

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (e Envelope) HasID() bool {
	trimmed := bytes.TrimSpace(e.ID)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func (e Envelope) IDKey() string {
	return string(bytes.TrimSpace(e.ID))
}

func (e Envelope) IsRequest() bool {
	return e.Method != "" && e.HasID()
}

func (e Envelope) IsNotification() bool {
	return e.Method != "" && !e.HasID()
}

func (e Envelope) IsResponse() bool {
	return e.Method == "" && e.HasID()
}

func (e Envelope) DecodeParams(dst any) error {
	if len(bytes.TrimSpace(e.Params)) == 0 {
		return fmt.Errorf("decode params for %q: missing params", e.Method)
	}
	if err := json.Unmarshal(e.Params, dst); err != nil {
		return fmt.Errorf("decode params for %q: %w", e.Method, err)
	}
	return nil
}

func (e Envelope) DecodeResult(dst any) error {
	if e.Error != nil {
		return e.Error
	}
	if len(bytes.TrimSpace(e.Result)) == 0 {
		return errors.New("decode result: missing result")
	}
	if err := json.Unmarshal(e.Result, dst); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

func MarshalID(id any) (json.RawMessage, error) {
	switch v := id.(type) {
	case json.RawMessage:
		trimmed := bytes.TrimSpace(v)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return nil, errors.New("jsonrpc id must be non-null")
		}
		return append(json.RawMessage(nil), trimmed...), nil
	case string, int, int64, uint64, float64:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal jsonrpc id: %w", err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported jsonrpc id type %T", id)
	}
}

func NewRequest(id any, method string, params any) (Envelope, error) {
	if method == "" {
		return Envelope{}, errors.New("jsonrpc request method is required")
	}
	rawID, err := MarshalID(id)
	if err != nil {
		return Envelope{}, err
	}
	var rawParams json.RawMessage
	if params != nil {
		rawParams, err = marshalRaw(params)
		if err != nil {
			return Envelope{}, fmt.Errorf("marshal params for %q: %w", method, err)
		}
	}
	return Envelope{
		JSONRPC: JSONRPCVersion,
		ID:      rawID,
		Method:  method,
		Params:  rawParams,
	}, nil
}

func NewNotification(method string, params any) (Envelope, error) {
	if method == "" {
		return Envelope{}, errors.New("jsonrpc notification method is required")
	}
	var rawParams json.RawMessage
	if params != nil {
		var err error
		rawParams, err = marshalRaw(params)
		if err != nil {
			return Envelope{}, fmt.Errorf("marshal params for %q: %w", method, err)
		}
	}
	return Envelope{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  rawParams,
	}, nil
}

func NewResponse(id json.RawMessage, result any) (Envelope, error) {
	rawID, err := MarshalID(id)
	if err != nil {
		return Envelope{}, err
	}
	if result == nil {
		result = map[string]any{}
	}
	rawResult, err := marshalRaw(result)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal result: %w", err)
	}
	return Envelope{
		JSONRPC: JSONRPCVersion,
		ID:      rawID,
		Result:  rawResult,
	}, nil
}

func NewErrorResponse(id json.RawMessage, code int, message string, data any) (Envelope, error) {
	var rawID json.RawMessage
	if trimmed := bytes.TrimSpace(id); len(trimmed) > 0 {
		rawID = append(json.RawMessage(nil), trimmed...)
	} else {
		rawID = json.RawMessage("null")
	}
	var rawData json.RawMessage
	if data != nil {
		var err error
		rawData, err = marshalRaw(data)
		if err != nil {
			return Envelope{}, fmt.Errorf("marshal error data: %w", err)
		}
	}
	return Envelope{
		JSONRPC: JSONRPCVersion,
		ID:      rawID,
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    rawData,
		},
	}, nil
}

func EncodeEnvelope(env Envelope) ([]byte, error) {
	if env.JSONRPC == "" {
		env.JSONRPC = JSONRPCVersion
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("encode jsonrpc envelope: %w", err)
	}
	return data, nil
}

func DecodeEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	if env.JSONRPC != JSONRPCVersion {
		return Envelope{}, fmt.Errorf("%w: got %q", ErrInvalidVersion, env.JSONRPC)
	}
	return env, nil
}

func marshalRaw(v any) (json.RawMessage, error) {
	if raw, ok := v.(json.RawMessage); ok {
		return append(json.RawMessage(nil), raw...), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
