package harness

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kai/internal/tools"
)

func callOf(id, name, path string) tools.Call {
	return tools.Call{ID: id, Name: name, Args: map[string]any{"path": path}}
}

// Independent calls run at the same time: four 100 ms calls take about 100 ms.
func TestRunBatchRunsIndependentCallsAtOnce(t *testing.T) {
	calls := []tools.Call{
		callOf("1", "read_file", "a"), callOf("2", "read_file", "b"),
		callOf("3", "read_file", "c"), callOf("4", "read_file", "d"),
	}
	start := time.Now()
	var got []string
	ok := runBatch(context.Background(), calls,
		func(_ int, c tools.Call) tools.Result {
			time.Sleep(100 * time.Millisecond)
			return tools.Result{ID: c.ID, Name: c.Name}
		},
		func(i int, r tools.Result) bool { got = append(got, r.ID); return true })
	if !ok {
		t.Fatal("batch was abandoned")
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("4 independent calls took %v, they should overlap", d)
	}
	if len(got) != 4 || got[0] != "1" || got[1] != "2" || got[2] != "3" || got[3] != "4" {
		t.Errorf("results out of call order: %v", got)
	}
}

// Results come back in call order even when a later call finishes first.
func TestRunBatchKeepsCallOrder(t *testing.T) {
	calls := []tools.Call{callOf("slow", "read_file", "a"), callOf("fast", "read_file", "b")}
	var got []string
	runBatch(context.Background(), calls,
		func(i int, c tools.Call) tools.Result {
			if i == 0 {
				time.Sleep(80 * time.Millisecond)
			}
			return tools.Result{ID: c.ID}
		},
		func(i int, r tools.Result) bool { got = append(got, r.ID); return true })
	if len(got) != 2 || got[0] != "slow" || got[1] != "fast" {
		t.Errorf("order = %v, want [slow fast]", got)
	}
}

// Calls on the same file, and shell commands, never overlap and keep order.
func TestRunBatchSerialisesSameFileAndShell(t *testing.T) {
	calls := []tools.Call{
		callOf("c", "create_file", "x.go"),
		callOf("e", "edit_file", "./x.go"),
		callOf("r", "read_file", "x.go"),
		{ID: "s1", Name: "shell", Args: map[string]any{"command": "a"}},
		{ID: "s2", Name: "shell", Args: map[string]any{"command": "b"}},
	}
	var running, maxSame atomic.Int32
	var mu sync.Mutex
	var order []string
	runBatch(context.Background(), calls,
		func(_ int, c tools.Call) tools.Result {
			lane := laneKey(c)
			if lane == "file:x.go" {
				if n := running.Add(1); n > maxSame.Load() {
					maxSame.Store(n)
				}
				defer running.Add(-1)
			}
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			order = append(order, c.ID)
			mu.Unlock()
			return tools.Result{ID: c.ID}
		},
		func(int, tools.Result) bool { return true })
	if maxSame.Load() != 1 {
		t.Errorf("calls on one file overlapped (%d at once)", maxSame.Load())
	}
	idx := map[string]int{}
	for i, id := range order {
		idx[id] = i
	}
	if !(idx["c"] < idx["e"] && idx["e"] < idx["r"]) {
		t.Errorf("same-file calls ran out of order: %v", order)
	}
	if idx["s1"] > idx["s2"] {
		t.Errorf("shell commands ran out of order: %v", order)
	}
}

// Stopping mid-batch returns false and does not hang.
func TestRunBatchStopsWhenDoneSaysSo(t *testing.T) {
	calls := []tools.Call{callOf("1", "read_file", "a"), callOf("2", "read_file", "b")}
	ok := runBatch(context.Background(), calls,
		func(_ int, c tools.Call) tools.Result { return tools.Result{ID: c.ID} },
		func(int, tools.Result) bool { return false })
	if ok {
		t.Error("runBatch should report the batch was abandoned")
	}
}

func TestRunBatchCancelledContextDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := []tools.Call{callOf("1", "read_file", "a")}
	done := make(chan bool, 1)
	go func() {
		done <- runBatch(ctx, calls,
			func(_ int, c tools.Call) tools.Result {
				<-ctx.Done()
				return tools.Result{ID: c.ID}
			},
			func(int, tools.Result) bool { return true })
	}()
	time.AfterFunc(30*time.Millisecond, cancel)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runBatch hung after the context was cancelled")
	}
}
