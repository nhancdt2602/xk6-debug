package debug

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/grafana/sobek"
	"github.com/nhancdt2602/xk6-debug/dap"
	"go.k6.io/k6/v2/js/modules"
)

// ModuleInstance is the per-VU instance of the debug module.
type ModuleInstance struct {
	vu           modules.VU
	bpm          *BreakpointManager
	dapServer    *dap.Server
	vuID         uint64
	vuIDResolved bool
	inited       bool
}

// Exports returns the named exports for `import { line, capture, breakpoint, enterScope } from 'k6/x/debug'`.
func (mi *ModuleInstance) Exports() modules.Exports {
	return modules.Exports{
		Named: map[string]any{
			"line":       mi.Line,
			"capture":    mi.Capture,
			"breakpoint": mi.Breakpoint,
			"enterScope": mi.EnterScope,
		},
	}
}

// EnterScope registers a scope's parent relationship and clears stale variables
// from a previous entry of this scope (e.g., previous iteration or loop pass).
func (mi *ModuleInstance) EnterScope(scopeID int, parentID int) {
	mi.ensureInit()
	mi.bpm.EnterScope(mi.vuID, scopeID, parentID)
}

func (mi *ModuleInstance) ensureInit() {
	// Always try to resolve VU ID — State() may not be available during
	// module init but becomes available once the VU starts executing.
	if !mi.vuIDResolved {
		if state := mi.vu.State(); state != nil {
			mi.vuID = state.VUID
			mi.vuIDResolved = true
			mi.bpm.EnsureVUState(mi.vuID)
		}
	}
	if mi.inited {
		return
	}
	mi.inited = true
	if !mi.vuIDResolved {
		mi.bpm.EnsureVUState(mi.vuID)
	}

	// In DAP mode: block until the IDE connects and sends configurationDone.
	// This prevents VU code from running before the user can set breakpoints.
	if mi.dapServer != nil {
		mi.dapServer.WaitForClient()
	}
}

// Line is called at the start of each instrumented source line.
// It handles breakpoint detection and step-over logic, pausing the VU if needed.
// location is {line, col, file, scope}.
func (mi *ModuleInstance) Line(location sobek.Value) {
	mi.ensureInit()

	if mi.dapServer == nil {
		return // no pause logic in CLI mode; captures handle output
	}
	if mi.bpm.IsDisconnected() {
		return
	}

	rt := mi.vu.Runtime()
	locObj := location.ToObject(rt)
	line := int(locObj.Get("line").ToInteger())
	file := locObj.Get("file").String()
	scopeID := int(locObj.Get("scope").ToInteger())

	steppingOverCurrentLine := mi.bpm.IsSteppingOver(mi.vuID, line)
	isStepping := mi.bpm.ShouldStepPause(mi.vuID, line)
	isPrimary := isStepping || (!steppingOverCurrentLine && mi.bpm.IsSet(file, line))
	isSecondary := !steppingOverCurrentLine && mi.bpm.ShouldPauseAll(mi.vuID)

	log.Printf("[k6-debug] VU#%d line=%d file=%s scope=%d isStepping=%v isPrimary=%v isSecondary=%v steppingOver=%v",
		mi.vuID, line, file, scopeID, isStepping, isPrimary, isSecondary, steppingOverCurrentLine)

	if isPrimary || isSecondary {
		if isPrimary {
			mi.bpm.RequestPauseAll()
		}
		mi.bpm.Pause(mi.vu.Context(), mi.vuID, file, line, scopeID, isPrimary)
	}
}

// Capture is called after each instrumented variable assignment.
// It only stores the variable value — all pause logic is handled by Line().
// location is {line, col, name, file, scope}, value is the JS variable value.
func (mi *ModuleInstance) Capture(location sobek.Value, value sobek.Value) {
	mi.ensureInit()

	rt := mi.vu.Runtime()
	locObj := location.ToObject(rt)
	name := locObj.Get("name").String()
	scopeID := int(locObj.Get("scope").ToInteger())

	mi.bpm.StoreVariable(mi.vuID, name, value, scopeID)

	// In CLI mode (no DAP), print captures to stderr as JSON
	if mi.dapServer == nil {
		line := locObj.Get("line").ToInteger()
		col := locObj.Get("col").ToInteger()
		file := locObj.Get("file").String()

		serialized := serializeValue(rt, value, defaultMaxDepth)
		entry := map[string]any{
			"type":  "capture",
			"vu":    mi.vuID,
			"file":  file,
			"line":  line,
			"col":   col,
			"name":  name,
			"value": serialized,
		}
		data, err := json.Marshal(entry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[k6-debug] serialize error: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "%s\n", data)
	}
}

// Breakpoint is called in place of `debugger` statements.
// location is {line, col, file, scope}.
func (mi *ModuleInstance) Breakpoint(location sobek.Value) {
	mi.ensureInit()

	rt := mi.vu.Runtime()
	locObj := location.ToObject(rt)
	line := int(locObj.Get("line").ToInteger())
	file := locObj.Get("file").String()
	scopeID := int(locObj.Get("scope").ToInteger())

	if mi.dapServer != nil {
		if mi.bpm.IsDisconnected() {
			return
		}
		if mi.bpm.HasAnyBreakpoints() && !mi.bpm.IsSet(file, line) {
			return
		}
	}

	if mi.dapServer == nil {
		fmt.Fprintf(os.Stderr, "[k6-debug] Breakpoint hit at %s:%d (VU #%d). Press Enter to continue...\n", file, line, mi.vuID)
	} else {
		mi.bpm.RequestPauseAll()
	}
	mi.bpm.Pause(mi.vu.Context(), mi.vuID, file, line, scopeID, true)
}
