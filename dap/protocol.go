package dap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Message represents a DAP protocol message (request, response, or event).
type Message struct {
	Seq     int    `json:"seq"`
	Type    string `json:"type"`              // "request", "response", "event"
	Command string `json:"command,omitempty"` // for requests and responses

	// Request fields
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// Response fields
	RequestSeq int    `json:"request_seq,omitempty"`
	Success    bool   `json:"success,omitempty"`
	Message    string `json:"message,omitempty"`
	Body       any    `json:"body,omitempty"`

	// Event fields
	Event string `json:"event,omitempty"`
}

// readMessage reads a DAP message from the reader.
// DAP uses HTTP-like framing: Content-Length header followed by JSON body.
func readMessage(reader *bufio.Reader) (*Message, error) {
	// Read headers
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // empty line separates headers from body
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, err = strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %s", val)
			}
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	// Read body
	body := make([]byte, contentLength)
	if _, err := readFull(reader, body); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("invalid DAP message: %w", err)
	}
	return &msg, nil
}

func readFull(reader *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := reader.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// marshalMessage serializes a DAP message to JSON bytes.
func marshalMessage(msg *Message) []byte {
	data, _ := json.Marshal(msg)
	return data
}

// NewResponse creates a DAP response for a given request.
func NewResponse(req *Message, body any) *Message {
	return &Message{
		Type:       "response",
		Command:    req.Command,
		RequestSeq: req.Seq,
		Success:    true,
		Body:       body,
	}
}

// NewErrorResponse creates a DAP error response.
func NewErrorResponse(req *Message, message string) *Message {
	return &Message{
		Type:       "response",
		Command:    req.Command,
		RequestSeq: req.Seq,
		Success:    false,
		Message:    message,
	}
}

// NewEvent creates a DAP event message.
func NewEvent(event string, body any) *Message {
	return &Message{
		Type:  "event",
		Event: event,
		Body:  body,
	}
}

// Common DAP body types

// Capabilities returned in initialize response.
type Capabilities struct {
	SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest"`
	SupportsFunctionBreakpoints      bool `json:"supportsFunctionBreakpoints"`
}

// Thread represents a DAP thread (maps to a k6 VU).
type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// StackFrame represents a DAP stack frame.
type StackFrame struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Source Source `json:"source"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// Source represents a DAP source file.
type Source struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// Scope represents a DAP variable scope.
type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	Expensive          bool   `json:"expensive"`
}

// Variable represents a DAP variable.
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// Breakpoint represents a verified DAP breakpoint.
type Breakpoint struct {
	ID       int    `json:"id"`
	Verified bool   `json:"verified"`
	Line     int    `json:"line"`
	Source   Source `json:"source,omitempty"`
}

// StoppedEventBody is the body for a "stopped" event.
type StoppedEventBody struct {
	Reason            string `json:"reason"`
	ThreadID          int    `json:"threadId"`
	AllThreadsStopped bool   `json:"allThreadsStopped,omitempty"`
}

// ContinuedEventBody is the body for a "continued" event.
type ContinuedEventBody struct {
	ThreadID            int  `json:"threadId"`
	AllThreadsContinued bool `json:"allThreadsContinued"`
}

// ThreadEventBody is the body for a "thread" event.
type ThreadEventBody struct {
	Reason   string `json:"reason"`
	ThreadID int    `json:"threadId"`
}

// SetBreakpointsArguments holds arguments for setBreakpoints request.
type SetBreakpointsArguments struct {
	Source      Source             `json:"source"`
	Breakpoints []SourceBreakpoint `json:"breakpoints"`
}

// SourceBreakpoint is a breakpoint location in source.
type SourceBreakpoint struct {
	Line int `json:"line"`
}

// StackTraceArguments holds arguments for stackTrace request.
type StackTraceArguments struct {
	ThreadID int `json:"threadId"`
}

// ScopesArguments holds arguments for scopes request.
type ScopesArguments struct {
	FrameID int `json:"frameId"`
}

// VariablesArguments holds arguments for variables request.
type VariablesArguments struct {
	VariablesReference int `json:"variablesReference"`
}

// ContinueArguments holds arguments for continue request.
type ContinueArguments struct {
	ThreadID int `json:"threadId"`
}

// NextArguments holds arguments for next request.
type NextArguments struct {
	ThreadID int `json:"threadId"`
}

// SourceArguments holds arguments for source request.
type SourceArguments struct {
	SourceReference int    `json:"sourceReference"`
	Source          Source `json:"source"`
}

// Capabilities returned in initialize response.
// (extended with source support)
type SourceBody struct {
	Content  string `json:"content"`
	MimeType string `json:"mimeType,omitempty"`
}
