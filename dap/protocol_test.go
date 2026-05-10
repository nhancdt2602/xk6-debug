package dap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestReadWriteMessage(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Seq:     1,
		Type:    "request",
		Command: "initialize",
	}

	data := marshalMessage(msg)
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(data), data)

	reader := bufio.NewReader(bytes.NewReader([]byte(frame)))
	parsed, err := readMessage(reader)
	if err != nil {
		t.Fatal(err)
	}

	if parsed.Seq != 1 {
		t.Errorf("expected seq 1, got %d", parsed.Seq)
	}
	if parsed.Type != "request" {
		t.Errorf("expected type request, got %s", parsed.Type)
	}
	if parsed.Command != "initialize" {
		t.Errorf("expected command initialize, got %s", parsed.Command)
	}
}

func TestReadMessageWithArguments(t *testing.T) {
	t.Parallel()

	args := SetBreakpointsArguments{
		Source:      Source{Path: "/tmp/test.js"},
		Breakpoints: []SourceBreakpoint{{Line: 10}},
	}
	argsJSON, _ := json.Marshal(args)

	msg := &Message{
		Seq:       2,
		Type:      "request",
		Command:   "setBreakpoints",
		Arguments: argsJSON,
	}

	data := marshalMessage(msg)
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(data), data)

	reader := bufio.NewReader(bytes.NewReader([]byte(frame)))
	parsed, err := readMessage(reader)
	if err != nil {
		t.Fatal(err)
	}

	var parsedArgs SetBreakpointsArguments
	if err := json.Unmarshal(parsed.Arguments, &parsedArgs); err != nil {
		t.Fatal(err)
	}
	if parsedArgs.Source.Path != "/tmp/test.js" {
		t.Errorf("expected path /tmp/test.js, got %s", parsedArgs.Source.Path)
	}
	if len(parsedArgs.Breakpoints) != 1 || parsedArgs.Breakpoints[0].Line != 10 {
		t.Errorf("unexpected breakpoints: %+v", parsedArgs.Breakpoints)
	}
}

func TestNewResponse(t *testing.T) {
	t.Parallel()

	req := &Message{Seq: 5, Type: "request", Command: "threads"}
	resp := NewResponse(req, map[string]any{"threads": []Thread{}})

	if resp.Type != "response" {
		t.Errorf("expected type response, got %s", resp.Type)
	}
	if resp.RequestSeq != 5 {
		t.Errorf("expected request_seq 5, got %d", resp.RequestSeq)
	}
	if resp.Command != "threads" {
		t.Errorf("expected command threads, got %s", resp.Command)
	}
	if !resp.Success {
		t.Error("expected success")
	}
}

func TestNewEvent(t *testing.T) {
	t.Parallel()

	event := NewEvent("stopped", StoppedEventBody{Reason: "breakpoint", ThreadID: 1})
	if event.Type != "event" {
		t.Errorf("expected type event, got %s", event.Type)
	}
	if event.Event != "stopped" {
		t.Errorf("expected event stopped, got %s", event.Event)
	}
}
