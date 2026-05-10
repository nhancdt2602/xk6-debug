package dap

import (
	"encoding/json"
	"testing"

	"github.com/grafana/sobek"
)

// mockBPManager implements BPManager for testing.
type mockBPManager struct {
	breakpoints  map[string][]int
	vus          []uint64
	vuState      VUState
	resumed      []uint64
	disconnected bool
}

func newMockBPM() *mockBPManager {
	return &mockBPManager{
		breakpoints: make(map[string][]int),
	}
}

func (m *mockBPManager) SetBreakpoints(file string, lines []int) {
	m.breakpoints[file] = lines
}

func (m *mockBPManager) Resume(vuID uint64, action int) {
	m.resumed = append(m.resumed, vuID)
}

func (m *mockBPManager) ResumeAll(action int) {
	m.resumed = append(m.resumed, 0) // sentinel
}

func (m *mockBPManager) SetDisconnected() {
	m.disconnected = true
}

func (m *mockBPManager) ListVUs() []uint64 {
	return m.vus
}

func (m *mockBPManager) GetVUState(vuID uint64) VUState {
	return m.vuState
}

func TestHandleInitialize(t *testing.T) {
	t.Parallel()
	h := NewHandler(newMockBPM())
	req := &Message{Seq: 1, Type: "request", Command: "initialize"}
	responses := h.HandleMessage(req)

	if len(responses) != 2 {
		t.Fatalf("expected 2 responses (response + initialized event), got %d", len(responses))
	}

	if responses[0].Type != "response" || responses[0].Command != "initialize" {
		t.Errorf("expected initialize response, got %s/%s", responses[0].Type, responses[0].Command)
	}
	if !responses[0].Success {
		t.Error("expected success")
	}

	if responses[1].Type != "event" || responses[1].Event != "initialized" {
		t.Errorf("expected initialized event, got %s/%s", responses[1].Type, responses[1].Event)
	}
}

func TestHandleLaunch(t *testing.T) {
	t.Parallel()
	h := NewHandler(newMockBPM())
	req := &Message{Seq: 1, Type: "request", Command: "launch"}
	responses := h.HandleMessage(req)
	if len(responses) != 1 || !responses[0].Success {
		t.Error("expected successful launch response")
	}
}

func TestHandleSetBreakpoints(t *testing.T) {
	t.Parallel()
	bpm := newMockBPM()
	h := NewHandler(bpm)

	args := SetBreakpointsArguments{
		Source:      Source{Path: "/tmp/test.js"},
		Breakpoints: []SourceBreakpoint{{Line: 10}, {Line: 20}},
	}
	argsJSON, _ := json.Marshal(args)

	req := &Message{Seq: 1, Type: "request", Command: "setBreakpoints", Arguments: argsJSON}
	responses := h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful setBreakpoints response")
	}

	// Check breakpoints were set on the manager
	if lines, ok := bpm.breakpoints["/tmp/test.js"]; !ok {
		t.Error("breakpoints not set on manager")
	} else if len(lines) != 2 {
		t.Errorf("expected 2 breakpoint lines, got %d", len(lines))
	}
}

func TestHandleThreads(t *testing.T) {
	t.Parallel()
	bpm := newMockBPM()
	bpm.vus = []uint64{1, 2, 3}
	h := NewHandler(bpm)

	req := &Message{Seq: 1, Type: "request", Command: "threads"}
	responses := h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful threads response")
	}

	body := responses[0].Body.(map[string]any)
	threads := body["threads"].([]Thread)
	if len(threads) != 3 {
		t.Errorf("expected 3 threads, got %d", len(threads))
	}
}

func TestHandleStackTrace(t *testing.T) {
	t.Parallel()
	bpm := newMockBPM()
	bpm.vuState = VUState{
		Paused:    true,
		PauseLine: 42,
		PauseFile: "/tmp/test.js",
	}
	h := NewHandler(bpm)

	args := StackTraceArguments{ThreadID: 1}
	argsJSON, _ := json.Marshal(args)

	req := &Message{Seq: 1, Type: "request", Command: "stackTrace", Arguments: argsJSON}
	responses := h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful stackTrace response")
	}

	body := responses[0].Body.(map[string]any)
	frames := body["stackFrames"].([]StackFrame)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if frames[0].Line != 42 {
		t.Errorf("expected line 42, got %d", frames[0].Line)
	}
}

func TestHandleScopesAndVariables(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	bpm := newMockBPM()
	bpm.vuState = VUState{
		Paused: true,
		Variables: []OrderedVar{
			{Name: "x", Value: rt.ToValue(42)},
			{Name: "s", Value: rt.ToValue("hello")},
		},
	}
	h := NewHandler(bpm)

	// Get scopes
	scopeArgs := ScopesArguments{FrameID: 1}
	scopeArgsJSON, _ := json.Marshal(scopeArgs)
	req := &Message{Seq: 1, Type: "request", Command: "scopes", Arguments: scopeArgsJSON}
	responses := h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful scopes response")
	}

	body := responses[0].Body.(map[string]any)
	scopes := body["scopes"].([]Scope)
	if len(scopes) != 1 {
		t.Fatalf("expected 1 scope, got %d", len(scopes))
	}
	scopeRef := scopes[0].VariablesReference

	// Get variables for the scope
	varArgs := VariablesArguments{VariablesReference: scopeRef}
	varArgsJSON, _ := json.Marshal(varArgs)
	req = &Message{Seq: 2, Type: "request", Command: "variables", Arguments: varArgsJSON}
	responses = h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful variables response")
	}

	body = responses[0].Body.(map[string]any)
	vars := body["variables"].([]Variable)
	if len(vars) != 2 {
		t.Errorf("expected 2 variables, got %d", len(vars))
	}
}

func TestHandleContinue(t *testing.T) {
	t.Parallel()
	bpm := newMockBPM()
	h := NewHandler(bpm)

	args := ContinueArguments{ThreadID: 5}
	argsJSON, _ := json.Marshal(args)
	req := &Message{Seq: 1, Type: "request", Command: "continue", Arguments: argsJSON}
	responses := h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful continue response")
	}
	// Continue resumes all threads
	if len(bpm.resumed) != 1 || bpm.resumed[0] != 0 {
		t.Errorf("expected ResumeAll (sentinel 0), got %v", bpm.resumed)
	}
}

func TestHandleDisconnect(t *testing.T) {
	t.Parallel()
	bpm := newMockBPM()
	h := NewHandler(bpm)

	req := &Message{Seq: 1, Type: "request", Command: "disconnect"}
	responses := h.HandleMessage(req)

	if len(responses) != 1 || !responses[0].Success {
		t.Fatal("expected successful disconnect response")
	}
	if !bpm.disconnected {
		t.Error("expected SetDisconnected to be called")
	}
}

func TestHandleUnknownCommand(t *testing.T) {
	t.Parallel()
	h := NewHandler(newMockBPM())
	req := &Message{Seq: 1, Type: "request", Command: "unknown"}
	responses := h.HandleMessage(req)
	if len(responses) != 1 || !responses[0].Success {
		t.Error("expected success response for unknown command (lenient handling)")
	}
}
