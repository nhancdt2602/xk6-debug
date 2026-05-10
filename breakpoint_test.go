package debug

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBreakpointRegistry(t *testing.T) {
	t.Parallel()
	bm := NewBreakpointManager()

	// No breakpoints set initially
	if bm.IsSet("test.js", 10) {
		t.Error("expected no breakpoint at test.js:10")
	}
	if bm.HasAnyBreakpoints() {
		t.Error("expected no breakpoints")
	}

	// Set breakpoints
	bm.SetBreakpoints("test.js", []int{10, 20, 30})
	if !bm.IsSet("test.js", 10) {
		t.Error("expected breakpoint at test.js:10")
	}
	if !bm.IsSet("test.js", 20) {
		t.Error("expected breakpoint at test.js:20")
	}
	if bm.IsSet("test.js", 15) {
		t.Error("expected no breakpoint at test.js:15")
	}
	if bm.IsSet("other.js", 10) {
		t.Error("expected no breakpoint at other.js:10")
	}
	if !bm.HasAnyBreakpoints() {
		t.Error("expected breakpoints to exist")
	}

	// Replace breakpoints for same file
	bm.SetBreakpoints("test.js", []int{15})
	if bm.IsSet("test.js", 10) {
		t.Error("expected no breakpoint at test.js:10 after replace")
	}
	if !bm.IsSet("test.js", 15) {
		t.Error("expected breakpoint at test.js:15")
	}
}

func TestVUPauseResume(t *testing.T) {
	t.Parallel()
	bm := NewBreakpointManager()
	bm.EnsureVUState(1)

	ctx := context.Background()

	var action ResumeAction
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		action = bm.Pause(ctx, 1, "test.js", 10, 0, true)
	}()

	// Give the goroutine time to block
	time.Sleep(50 * time.Millisecond)

	state := bm.GetVUState(1)
	if !state.Paused {
		t.Error("expected VU to be paused")
	}

	bm.Resume(1, ActionContinue)
	wg.Wait()

	if action != ActionContinue {
		t.Errorf("expected ActionContinue, got %d", action)
	}
}

func TestVUPauseContextCancel(t *testing.T) {
	t.Parallel()
	bm := NewBreakpointManager()
	bm.EnsureVUState(1)

	ctx, cancel := context.WithCancel(context.Background())

	var action ResumeAction
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		action = bm.Pause(ctx, 1, "test.js", 10, 0, true)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()

	if action != ActionContinue {
		t.Errorf("expected ActionContinue on cancel, got %d", action)
	}
}

func TestResumeAll(t *testing.T) {
	t.Parallel()
	bm := NewBreakpointManager()
	bm.EnsureVUState(1)
	bm.EnsureVUState(2)

	ctx := context.Background()
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		bm.Pause(ctx, 1, "test.js", 10, 0, true)
	}()
	go func() {
		defer wg.Done()
		bm.Pause(ctx, 2, "test.js", 20, 0, true)
	}()

	time.Sleep(50 * time.Millisecond)
	bm.ResumeAll(ActionContinue)
	wg.Wait()
}

func TestListVUs(t *testing.T) {
	t.Parallel()
	bm := NewBreakpointManager()
	bm.EnsureVUState(1)
	bm.EnsureVUState(2)
	bm.EnsureVUState(3)

	vus := bm.ListVUs()
	if len(vus) != 3 {
		t.Errorf("expected 3 VUs, got %d", len(vus))
	}

	bm.RemoveVU(2)
	vus = bm.ListVUs()
	if len(vus) != 2 {
		t.Errorf("expected 2 VUs after removal, got %d", len(vus))
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	bm := NewBreakpointManager()

	var wg sync.WaitGroup
	for i := uint64(0); i < 100; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			bm.EnsureVUState(id)
			bm.SetBreakpoints("test.js", []int{int(id)})
			bm.IsSet("test.js", int(id))
			bm.ListVUs()
		}(i)
	}
	wg.Wait()
}
