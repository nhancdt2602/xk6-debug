package dap

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/grafana/sobek"
)

// BPManager defines the interface the DAP handler needs from the breakpoint manager.
type BPManager interface {
	SetBreakpoints(file string, lines []int)
	Resume(vuID uint64, action int)
	ResumeAll(action int)
	SetDisconnected()
	ListVUs() []uint64
	GetVUState(vuID uint64) VUState
}

// OrderedVar is a variable with its name, preserving insertion order.
type OrderedVar struct {
	Name  string
	Value sobek.Value
}

// VUState provides a snapshot of VU debug state for DAP.
type VUState struct {
	Paused    bool
	PauseLine int
	PauseFile string
	Variables []OrderedVar
}

// Handler processes DAP requests and produces responses.
type Handler struct {
	mu       sync.Mutex
	bpm      BPManager
	server   *Server
	varRefs  *VariableRef
	vuScopes map[uint64]int // vuID → variablesReference for scope

	// ActionContinue, ActionNext, ActionStepIn constants matching breakpoint.go
	actionContinue int
	actionNext     int

	// scriptDir is the working directory, used to resolve relative file paths.
	scriptDir string
}

// NewHandler creates a new DAP handler.
func NewHandler(bpm BPManager) *Handler {
	cwd, _ := os.Getwd()
	return &Handler{
		bpm:            bpm,
		varRefs:        NewVariableRef(),
		vuScopes:       make(map[uint64]int),
		actionContinue: 0,
		actionNext:     1,
		scriptDir:      cwd,
	}
}

// resolveAbsPath resolves a potentially relative file path to absolute.
func (h *Handler) resolveAbsPath(file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(h.scriptDir, file)
}

// HandleMessage dispatches a DAP request and returns response messages.
func (h *Handler) HandleMessage(msg *Message) []*Message {
	if msg.Type != "request" {
		return nil
	}

	switch msg.Command {
	case "initialize":
		return h.handleInitialize(msg)
	case "launch", "attach":
		return h.handleLaunch(msg)
	case "setBreakpoints":
		return h.handleSetBreakpoints(msg)
	case "configurationDone":
		return h.handleConfigurationDone(msg)
	case "threads":
		return h.handleThreads(msg)
	case "stackTrace":
		return h.handleStackTrace(msg)
	case "scopes":
		return h.handleScopes(msg)
	case "variables":
		return h.handleVariables(msg)
	case "continue":
		return h.handleContinue(msg)
	case "next":
		return h.handleNext(msg)
	case "source":
		return h.handleSource(msg)
	case "disconnect":
		return h.handleDisconnect(msg)

	// Commands that VS Code sends during initialization that we don't need
	// but must respond to successfully, otherwise VS Code disconnects.
	case "setExceptionBreakpoints",
		"loadedSources",
		"modules",
		"setFunctionBreakpoints",
		"setDataBreakpoints",
		"setInstructionBreakpoints",
		"pause",
		"evaluate",
		"completions",
		"stepIn",
		"stepOut",
		"reverseContinue",
		"restartFrame",
		"goto",
		"terminateThreads",
		"readMemory",
		"writeMemory",
		"cancel",
		"breakpointLocations":
		log.Printf("[k6-debug] Acknowledged DAP command: %s", msg.Command)
		return []*Message{NewResponse(msg, map[string]any{})}

	default:
		log.Printf("[k6-debug] Unhandled DAP command: %s", msg.Command)
		return []*Message{NewResponse(msg, map[string]any{})}
	}
}

func (h *Handler) handleInitialize(msg *Message) []*Message {
	resp := NewResponse(msg, Capabilities{
		SupportsConfigurationDoneRequest: true,
		SupportsFunctionBreakpoints:      false,
	})
	initialized := NewEvent("initialized", nil)
	return []*Message{resp, initialized}
}

func (h *Handler) handleLaunch(msg *Message) []*Message {
	// k6 is already running; nothing to launch.
	return []*Message{NewResponse(msg, nil)}
}

func (h *Handler) handleConfigurationDone(msg *Message) []*Message {
	// Signal that the client is ready — unblocks WaitForClient().
	if h.server != nil {
		h.server.SignalReady()
	}
	return []*Message{NewResponse(msg, nil)}
}

func (h *Handler) handleSource(msg *Message) []*Message {
	var args SourceArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid source arguments")}
	}

	file := args.Source.Path
	if file == "" {
		file = args.Source.Name
	}
	absPath := h.resolveAbsPath(file)

	content, err := os.ReadFile(absPath)
	if err != nil {
		return []*Message{NewErrorResponse(msg, fmt.Sprintf("could not read source: %s", absPath))}
	}

	body := SourceBody{
		Content:  string(content),
		MimeType: "text/javascript",
	}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleSetBreakpoints(msg *Message) []*Message {
	var args SetBreakpointsArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid setBreakpoints arguments")}
	}

	file := args.Source.Path
	if file == "" {
		file = args.Source.Name
	}

	lines := make([]int, len(args.Breakpoints))
	verified := make([]Breakpoint, len(args.Breakpoints))
	for i, bp := range args.Breakpoints {
		lines[i] = bp.Line
		verified[i] = Breakpoint{
			ID:       i + 1,
			Verified: true,
			Line:     bp.Line,
			Source: Source{
				Name: filepath.Base(file),
				Path: file,
			},
		}
	}

	h.bpm.SetBreakpoints(file, lines)

	body := map[string]any{"breakpoints": verified}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleThreads(msg *Message) []*Message {
	vuIDs := h.bpm.ListVUs()
	threads := make([]Thread, len(vuIDs))
	for i, id := range vuIDs {
		threads[i] = Thread{
			ID:   int(id),
			Name: fmt.Sprintf("VU #%d", id),
		}
	}
	body := map[string]any{"threads": threads}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleStackTrace(msg *Message) []*Message {
	var args StackTraceArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid stackTrace arguments")}
	}

	vuID := uint64(args.ThreadID)
	state := h.bpm.GetVUState(vuID)

	var frames []StackFrame
	if state.Paused {
		absPath := h.resolveAbsPath(state.PauseFile)
		frames = []StackFrame{
			{
				ID:   args.ThreadID,
				Name: fmt.Sprintf("VU #%d", vuID),
				Source: Source{
					Name: filepath.Base(state.PauseFile),
					Path: absPath,
				},
				Line:   state.PauseLine,
				Column: 0,
			},
		}
	}

	body := map[string]any{
		"stackFrames": frames,
		"totalFrames": len(frames),
	}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleScopes(msg *Message) []*Message {
	var args ScopesArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid scopes arguments")}
	}

	h.mu.Lock()
	// Use frame ID (which equals thread/VU ID) as the scope reference base.
	// We assign a unique variablesReference for this VU's scope.
	vuID := uint64(args.FrameID)
	scopeRef := h.varRefs.Add(nil) // reserve a ref ID for this scope
	h.vuScopes[vuID] = scopeRef
	h.mu.Unlock()

	scopes := []Scope{
		{
			Name:               "Local Variables",
			VariablesReference: scopeRef,
			Expensive:          false,
		},
	}
	body := map[string]any{"scopes": scopes}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleVariables(msg *Message) []*Message {
	var args VariablesArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid variables arguments")}
	}

	h.mu.Lock()
	// Check if this is a scope reference (top-level variables for a VU)
	var vuID uint64
	isScopeRef := false
	for vid, scopeRef := range h.vuScopes {
		if scopeRef == args.VariablesReference {
			vuID = vid
			isScopeRef = true
			break
		}
	}
	h.mu.Unlock()

	var variables []Variable

	if isScopeRef {
		// Top-level: return all captured variables for this VU (in capture order)
		state := h.bpm.GetVUState(vuID)
		for _, ov := range state.Variables {
			v := Variable{
				Name:  ov.Name,
				Value: formatValue(ov.Value),
				Type:  valueType(ov.Value),
			}
			if obj, ok := ov.Value.(*sobek.Object); ok {
				if _, isFunc := sobek.AssertFunction(ov.Value); !isFunc {
					v.VariablesReference = h.varRefs.Add(obj)
				}
			}
			variables = append(variables, v)
		}
	} else {
		// Nested: return properties of the referenced object
		obj := h.varRefs.Get(args.VariablesReference)
		if obj != nil {
			for _, key := range obj.Keys() {
				child := obj.Get(key)
				v := Variable{
					Name:  key,
					Value: formatValue(child),
					Type:  valueType(child),
				}
				if childObj, ok := child.(*sobek.Object); ok {
					if _, isFunc := sobek.AssertFunction(child); !isFunc {
						v.VariablesReference = h.varRefs.Add(childObj)
					}
				}
				variables = append(variables, v)
			}
		}
	}

	body := map[string]any{"variables": variables}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleContinue(msg *Message) []*Message {
	var args ContinueArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid continue arguments")}
	}

	h.varRefs.Clear()
	// Continue resumes ALL threads — they all run until the next breakpoint.
	h.bpm.ResumeAll(h.actionContinue)

	body := map[string]any{"allThreadsContinued": true}
	return []*Message{NewResponse(msg, body)}
}

func (h *Handler) handleNext(msg *Message) []*Message {
	var args NextArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil {
		return []*Message{NewErrorResponse(msg, "invalid next arguments")}
	}

	h.varRefs.Clear()

	// Tell VS Code all threads continued so it clears all pause highlights.
	// When the stepping thread stops again, only its highlight will appear.
	continued := NewEvent("continued", ContinuedEventBody{
		ThreadID:            args.ThreadID,
		AllThreadsContinued: true,
	})

	// Next resumes ONLY the active thread — others stay frozen.
	h.bpm.Resume(uint64(args.ThreadID), h.actionNext)

	return []*Message{NewResponse(msg, nil), continued}
}

func (h *Handler) handleDisconnect(msg *Message) []*Message {
	// Unblock WaitForClient in case it's still waiting.
	if h.server != nil {
		h.server.SignalReady()
	}
	// Mark disconnected: resumes all currently paused VUs and prevents future pauses.
	h.bpm.SetDisconnected()
	return []*Message{NewResponse(msg, nil)}
}

// SendStoppedEvent sends a stopped event to the IDE.
func (h *Handler) SendStoppedEvent(vuID uint64) {
	if h.server == nil {
		return
	}
	log.Printf("[k6-debug] Sending stopped event for VU #%d", vuID)
	event := NewEvent("stopped", StoppedEventBody{
		Reason:   "breakpoint",
		ThreadID: int(vuID),
	})
	if err := h.server.SendEvent(event); err != nil {
		log.Printf("[k6-debug] Failed to send stopped event: %v", err)
	}
}

// SendThreadEvent sends a thread started/exited event to the IDE.
func (h *Handler) SendThreadEvent(vuID uint64, reason string) {
	if h.server == nil {
		return
	}
	event := NewEvent("thread", ThreadEventBody{
		Reason:   reason,
		ThreadID: int(vuID),
	})
	if err := h.server.SendEvent(event); err != nil {
		log.Printf("[k6-debug] Failed to send thread event: %v", err)
	}
}

// SendTerminatedEvent sends a terminated event to the IDE.
func (h *Handler) SendTerminatedEvent() {
	if h.server == nil {
		return
	}
	event := NewEvent("terminated", nil)
	_ = h.server.SendEvent(event)
}

func formatValue(v sobek.Value) string {
	if v == nil || sobek.IsUndefined(v) {
		return "undefined"
	}
	if sobek.IsNull(v) {
		return "null"
	}
	if _, isFunc := sobek.AssertFunction(v); isFunc {
		return "[Function]"
	}
	return v.String()
}

func valueType(v sobek.Value) string {
	if v == nil || sobek.IsUndefined(v) {
		return "undefined"
	}
	if sobek.IsNull(v) {
		return "null"
	}
	if _, isFunc := sobek.AssertFunction(v); isFunc {
		return "function"
	}
	if obj, ok := v.(*sobek.Object); ok {
		return obj.ClassName()
	}
	return "string"
}
