// Package debug provides a k6 extension module for debugging JavaScript test scripts.
// It exports `capture` and `breakpoint` functions that are injected by the Babel preprocessor.
//
// Usage:
//
//	import { capture, breakpoint } from 'k6/x/debug';
//
// When K6_DEBUG_DAP environment variable is set (e.g., K6_DEBUG_DAP=:4711), a DAP
// server is started for IDE integration. Otherwise, debug output goes to stderr
// and breakpoints can be resumed via stdin.
package debug

import (
	"os"
	"sync"

	"github.com/nhancdt2602/xk6-debug/dap"
	"go.k6.io/k6/v2/js/modules"
)

func init() {
	modules.Register("k6/x/debug", New())
}

// RootModule is the top-level module, created once per test run.
type RootModule struct {
	bpm       *BreakpointManager
	dapServer *dap.Server
	handler   *dap.Handler
	once      sync.Once
}

// New creates a new RootModule.
func New() *RootModule {
	return &RootModule{}
}

// NewModuleInstance creates a per-VU module instance.
func (rm *RootModule) NewModuleInstance(vu modules.VU) modules.Instance {
	rm.once.Do(func() {
		rm.bpm = NewBreakpointManager()

		if addr := os.Getenv("K6_DEBUG_DAP"); addr != "" {
			adapter := &bpmAdapter{bpm: rm.bpm}
			rm.handler = dap.NewHandler(adapter)
			rm.dapServer = dap.NewServer(addr, rm.handler)

			// Wire DAP events
			rm.bpm.SetOnStopped(func(vuID uint64, file string, line int) {
				rm.handler.SendStoppedEvent(vuID)
			})
			rm.bpm.SetOnThread(func(vuID uint64, reason string) {
				rm.handler.SendThreadEvent(vuID, reason)
			})

			go rm.dapServer.Start() //nolint:errcheck
		} else {
			go rm.bpm.ListenStdinResume()
		}
	})

	return &ModuleInstance{vu: vu, bpm: rm.bpm, dapServer: rm.dapServer}
}

var (
	_ modules.Module   = &RootModule{}
	_ modules.Instance = &ModuleInstance{}
)

// bpmAdapter adapts BreakpointManager to the dap.BPManager interface.
type bpmAdapter struct {
	bpm *BreakpointManager
}

func (a *bpmAdapter) SetBreakpoints(file string, lines []int) {
	a.bpm.SetBreakpoints(file, lines)
}

func (a *bpmAdapter) Resume(vuID uint64, action int) {
	a.bpm.Resume(vuID, ResumeAction(action))
}

func (a *bpmAdapter) ResumeAll(action int) {
	a.bpm.ResumeAll(ResumeAction(action))
}

func (a *bpmAdapter) SetDisconnected() {
	a.bpm.SetDisconnected()
}

func (a *bpmAdapter) ListVUs() []uint64 {
	return a.bpm.ListVUs()
}

func (a *bpmAdapter) GetVUState(vuID uint64) dap.VUState {
	state := a.bpm.GetVUState(vuID)
	if state == nil {
		return dap.VUState{}
	}
	visible := a.bpm.GetVisibleVariables(vuID)
	vars := make([]dap.OrderedVar, len(visible))
	for i, v := range visible {
		vars[i] = dap.OrderedVar{Name: v.Name, Value: v.Value}
	}
	return dap.VUState{
		Paused:    state.Paused,
		PauseLine: state.PauseLine,
		PauseFile: state.PauseFile,
		Variables: vars,
	}
}
