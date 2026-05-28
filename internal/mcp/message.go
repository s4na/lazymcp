package mcp

import "encoding/json"

type Message struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewResult(id any, result any) Message {
	return Message{JSONRPC: "2.0", ID: id, Result: result}
}

func NewError(id any, code int, message string) Message {
	return Message{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}}
}

func NewRequest(id any, method string, params json.RawMessage) Message {
	return Message{JSONRPC: "2.0", ID: id, Method: method, Params: params}
}

func NewNotification(method string, params any) Message {
	raw, _ := json.Marshal(params)
	return Message{JSONRPC: "2.0", Method: method, Params: raw}
}
