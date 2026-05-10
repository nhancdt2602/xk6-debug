package debug

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/grafana/sobek"
)

// ResumeAction represents the action to take when resuming a paused VU.
type ResumeAction int

const (
	// ActionContinue resumes execution until next breakpoint.
	ActionContinue ResumeAction = iota
	// ActionNext steps to the next instrumented statement (behaves like continue for now).
	ActionNext
	// ActionStepIn steps into the next instrumented statement (behaves like continue for now).
	ActionStepIn
)

// ScopedVariable pairs a captured value with its lexical scope ID.
type ScopedVariable struct {
	Value   sobek.Value
	ScopeID int
}

// VUDebugState holds the debug state for a single VU.
type VUDebugState struct {
	Resume     chan ResumeAction
	Paused     bool
	PauseLine  int
	PauseFile  string
	PauseScope int                        // scope ID at the pause point
	Variables  map[string]ScopedVariable   // variable name → scoped value
	VarOrder   []string                   // insertion order of variable names
	LastAction ResumeAction               // the action that last resumed this VU
}

// BreakpointManager manages breakpoints and per-VU debug state.
type BreakpointManager struct {
	mu           sync.RWMutex
	breakpoints  map[string]map[int]bool // file → line → enabled
	vuStates     map[uint64]*VUDebugState
	scopeParents map[int]int             // scopeID → parentScopeID (shared across VUs)
	disconnected bool                    // set when DAP client disconnects; prevents future pauses
	allStopped   bool                    // true when all VUs should stop at next instrumented statement
	stopClaimed  bool                    // true when a VU has already sent the stopped event for this round

	// Callbacks for DAP events
	onStopped func(vuID uint64, file string, line int)
	onThread  func(vuID uint64, reason string) // "started" or "exited"
}

// NewBreakpointManager creates a new BreakpointManager.
func NewBreakpointManager() *BreakpointManager {
	return &BreakpointManager{
		breakpoints:  make(map[string]map[int]bool),
		vuStates:     make(map[uint64]*VUDebugState),
		scopeParents: make(map[int]int),
	}
}

// SetOnStopped sets a callback invoked when a VU pauses at a breakpoint.
func (bm *BreakpointManager) SetOnStopped(fn func(vuID uint64, file string, line int)) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.onStopped = fn
}

// SetOnThread sets a callback invoked when a VU starts or exits.
func (bm *BreakpointManager) SetOnThread(fn func(vuID uint64, reason string)) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.onThread = fn
}

// SetBreakpoints sets the active breakpoints for a file. Replaces any previous breakpoints for that file.
func (bm *BreakpointManager) SetBreakpoints(file string, lines []int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	log.Printf("[k6-debug] SetBreakpoints: file=%q lines=%v (current breakpoints: %v)", file, lines, bm.breakpoints)
	if len(lines) == 0 {
		delete(bm.breakpoints, file)
		return
	}
	lineSet := make(map[int]bool, len(lines))
	for _, l := range lines {
		lineSet[l] = true
	}
	bm.breakpoints[file] = lineSet
}

// IsSet checks if a breakpoint is set at the given file and line.
// It first tries an exact path match, then falls back to basename matching
// to handle the mismatch between VS Code's absolute paths and the
// preprocessor's relative filenames.
func (bm *BreakpointManager) IsSet(file string, line int) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	// Exact match first.
	if lines, ok := bm.breakpoints[file]; ok {
		return lines[line]
	}
	// Fallback: match by basename (e.g., "sample.js" matches "/abs/path/sample.js").
	base := filepath.Base(file)
	for bpFile, lines := range bm.breakpoints {
		if filepath.Base(bpFile) == base {
			return lines[line]
		}
	}
	return false
}

// HasAnyBreakpoints returns true if any breakpoints have been set via DAP.
func (bm *BreakpointManager) HasAnyBreakpoints() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return len(bm.breakpoints) > 0
}

// EnsureVUState initializes VU state if it doesn't exist.
func (bm *BreakpointManager) EnsureVUState(vuID uint64) {
	bm.mu.Lock()
	if _, exists := bm.vuStates[vuID]; exists {
		bm.mu.Unlock()
		return
	}
	bm.vuStates[vuID] = &VUDebugState{
		Resume:    make(chan ResumeAction, 1),
		Variables: make(map[string]ScopedVariable),
	}
	onThread := bm.onThread
	bm.mu.Unlock()
	// Call synchronously after releasing the lock so the thread event is
	// delivered to VS Code before any subsequent stopped event.
	if onThread != nil {
		onThread(vuID, "started")
	}
}

// RemoveVU cleans up state for a VU.
func (bm *BreakpointManager) RemoveVU(vuID uint64) {
	bm.mu.Lock()
	delete(bm.vuStates, vuID)
	onThread := bm.onThread
	bm.mu.Unlock()
	if onThread != nil {
		onThread(vuID, "exited")
	}
}

// EnterScope registers a scope's parent relationship and clears any variables
// from a previous entry of this scope (e.g., previous loop iteration or function call).
func (bm *BreakpointManager) EnterScope(vuID uint64, scopeID int, parentID int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.scopeParents[scopeID] = parentID
	if state, ok := bm.vuStates[vuID]; ok {
		cleared := make(map[string]bool)
		for name, sv := range state.Variables {
			if sv.ScopeID == scopeID {
				delete(state.Variables, name)
				cleared[name] = true
			}
		}
		if len(cleared) > 0 {
			filtered := state.VarOrder[:0]
			for _, name := range state.VarOrder {
				if !cleared[name] {
					filtered = append(filtered, name)
				}
			}
			state.VarOrder = filtered
		}
	}
}

// StoreVariable stores a captured variable for a VU with its scope ID.
func (bm *BreakpointManager) StoreVariable(vuID uint64, name string, value sobek.Value, scopeID int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if state, ok := bm.vuStates[vuID]; ok {
		if _, exists := state.Variables[name]; !exists {
			state.VarOrder = append(state.VarOrder, name)
		}
		state.Variables[name] = ScopedVariable{Value: value, ScopeID: scopeID}
	}
}

// scopeChain returns the set of scope IDs that are ancestors of (or equal to) the given scope.
// Must be called with bm.mu held.
func (bm *BreakpointManager) scopeChain(scopeID int) map[int]bool {
	chain := map[int]bool{scopeID: true}
	current := scopeID
	for {
		parent, ok := bm.scopeParents[current]
		if !ok || chain[parent] {
			break
		}
		chain[parent] = true
		current = parent
	}
	return chain
}

// VisibleVar is a variable name-value pair returned in capture order.
type VisibleVar struct {
	Name  string
	Value sobek.Value
}

// GetVisibleVariables returns variables that are in scope at the VU's current
// pause point, in the order they were first captured.
func (bm *BreakpointManager) GetVisibleVariables(vuID uint64) []VisibleVar {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	state, ok := bm.vuStates[vuID]
	if !ok {
		return nil
	}
	chain := bm.scopeChain(state.PauseScope)
	var result []VisibleVar
	for _, name := range state.VarOrder {
		sv, exists := state.Variables[name]
		if exists && chain[sv.ScopeID] {
			result = append(result, VisibleVar{Name: name, Value: sv.Value})
		}
	}
	return result
}

// IsDisconnected returns true if the DAP client has disconnected.
func (bm *BreakpointManager) IsDisconnected() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.disconnected
}

// SetDisconnected marks the client as disconnected and resumes all paused VUs.
// After this, Pause() becomes a no-op to prevent VUs from blocking.
func (bm *BreakpointManager) SetDisconnected() {
	bm.mu.Lock()
	bm.disconnected = true
	bm.mu.Unlock()
	bm.ResumeAll(ActionContinue)
}

// Pause blocks the VU goroutine until resumed or context is cancelled.
// If notify is true, the onStopped callback is invoked (sends DAP stopped event),
// but only if this VU is the first to stop in this round (allStopped not yet set).
// If another VU already triggered the stop, this VU pauses silently.
// Returns the resume action taken, or ActionContinue if context was cancelled.
func (bm *BreakpointManager) Pause(ctx context.Context, vuID uint64, file string, line int, scopeID int, notify bool) ResumeAction {
	bm.mu.Lock()
	if bm.disconnected {
		bm.mu.Unlock()
		return ActionContinue
	}
	state, ok := bm.vuStates[vuID]
	if !ok {
		bm.mu.Unlock()
		return ActionContinue
	}
	state.Paused = true
	state.PauseLine = line
	state.PauseFile = file
	state.PauseScope = scopeID

	// Only the first VU to stop in a round sends the stopped event.
	// If stopClaimed is already true, another VU already sent the event.
	shouldNotify := notify && !bm.stopClaimed
	if shouldNotify {
		bm.stopClaimed = true
	}
	onStopped := bm.onStopped
	bm.mu.Unlock()

	if shouldNotify && onStopped != nil {
		onStopped(vuID, file, line)
	}

	select {
	case action := <-state.Resume:
		bm.mu.Lock()
		state.Paused = false
		state.LastAction = action
		bm.mu.Unlock()
		return action
	case <-ctx.Done():
		bm.mu.Lock()
		state.Paused = false
		state.LastAction = ActionContinue
		bm.mu.Unlock()
		return ActionContinue
	}
}

// Resume unblocks a paused VU with the given action.
func (bm *BreakpointManager) Resume(vuID uint64, action ResumeAction) {
	bm.mu.Lock()
	bm.stopClaimed = false
	bm.mu.Unlock()
	bm.mu.RLock()
	state, ok := bm.vuStates[vuID]
	bm.mu.RUnlock()
	if ok && state.Paused {
		select {
		case state.Resume <- action:
		default:
		}
	}
}

// ResumeAll resumes all paused VUs and clears the stop flags.
func (bm *BreakpointManager) ResumeAll(action ResumeAction) {
	bm.mu.Lock()
	bm.allStopped = false
	bm.stopClaimed = false
	bm.mu.Unlock()
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	for _, state := range bm.vuStates {
		if state.Paused {
			select {
			case state.Resume <- action:
			default:
			}
		}
	}
}

// RequestPauseAll sets the allStopped flag so all VUs pause at their next
// instrumented statement. Called when one VU hits a breakpoint.
func (bm *BreakpointManager) RequestPauseAll() {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.allStopped = true
}

// ShouldPauseAll returns true if the VU should pause because another VU
// triggered a breakpoint (all-stop mode). Only returns true for VUs that
// aren't already paused.
func (bm *BreakpointManager) ShouldPauseAll(vuID uint64) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	if !bm.allStopped {
		return false
	}
	state, ok := bm.vuStates[vuID]
	if !ok || state.Paused {
		return false
	}
	return true
}

// ShouldStepPause checks if the VU should pause due to a pending step (next/stepIn).
// It only triggers when the current line differs from where the VU was previously paused,
// so multiple captures on the same source line (e.g. variable + __line marker) don't
// cause duplicate pauses. Clears the stepping flag when it returns true.
func (bm *BreakpointManager) ShouldStepPause(vuID uint64, line int) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	state, ok := bm.vuStates[vuID]
	if !ok {
		return false
	}
	if state.LastAction == ActionNext || state.LastAction == ActionStepIn {
		// Still on the same line as the previous pause — don't stop yet.
		if line == state.PauseLine {
			return false
		}
		state.LastAction = ActionContinue
		return true
	}
	return false
}

// IsSteppingOver returns true if the VU is currently in a step operation and
// the given line is the same as where it last paused. This prevents breakpoints
// on the current line from re-triggering immediately after a step resumes.
func (bm *BreakpointManager) IsSteppingOver(vuID uint64, line int) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	state, ok := bm.vuStates[vuID]
	if !ok {
		return false
	}
	return (state.LastAction == ActionNext || state.LastAction == ActionStepIn) && line == state.PauseLine
}

// GetVUState returns the debug state for a VU.
func (bm *BreakpointManager) GetVUState(vuID uint64) *VUDebugState {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.vuStates[vuID]
}

// ListVUs returns all known VU IDs.
func (bm *BreakpointManager) ListVUs() []uint64 {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	ids := make([]uint64, 0, len(bm.vuStates))
	for id := range bm.vuStates {
		ids = append(ids, id)
	}
	return ids
}

// ListenStdinResume reads lines from stdin and resumes all paused VUs on each line.
// This is the fallback mode when no DAP server is active.
func (bm *BreakpointManager) ListenStdinResume() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fmt.Fprintln(os.Stderr, "[k6-debug] Resuming all paused VUs...")
		bm.ResumeAll(ActionContinue)
	}
}
