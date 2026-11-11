package harness

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"kai/internal/tools"
)

// Parallel tool calling. A reply may hold several tool calls; they are run at
// the same time so a batch of reads, commands or new files costs as much as
// the slowest one, not the sum.
//
// Two rules keep it safe:
//
//   - Calls that touch the same file form a lane and run one after another in
//     the order the model wrote them (create a file, then edit it, then read
//     it back in one reply works). Shell commands share one lane too, so
//     "mkdir x" and "cd x && make" can never overlap. Everything else runs
//     at once.
//   - Results are handed back strictly in call order, each one as soon as it
//     and every call before it is done. The model and the timeline therefore
//     always see the same order the calls were written in, however the
//     goroutines finish.

// maxParallel bounds how many lanes run at the same time.
const maxParallel = 8

// laneKey says which lane a call belongs to. "" means its own lane.
func laneKey(c tools.Call) string {
	switch c.Name {
	case "shell":
		return "shell"
	case "read_file", "create_file", "edit_file":
		p, _ := tools.StringArg(c.Args, "path")
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		return "file:" + p
	}
	return ""
}

// runBatch runs the calls concurrently (see the rules above). run executes one
// call. done is called for each call, in call order, on the caller's
// goroutine, as soon as its result is ready; if it returns false the batch is
// abandoned. runBatch returns false when it was abandoned or ctx ended.
func runBatch(ctx context.Context, calls []tools.Call, run func(i int, c tools.Call) tools.Result, done func(i int, r tools.Result) bool) bool {
	n := len(calls)
	if n == 0 {
		return true
	}

	// Lanes keep the order of their calls.
	lanes := make([][]int, 0, n)
	byKey := map[string]int{}
	for i, c := range calls {
		k := laneKey(c)
		if k == "" {
			lanes = append(lanes, []int{i})
			continue
		}
		if li, ok := byKey[k]; ok {
			lanes[li] = append(lanes[li], i)
			continue
		}
		byKey[k] = len(lanes)
		lanes = append(lanes, []int{i})
	}

	type finished struct {
		i int
		r tools.Result
	}
	results := make(chan finished, n) // big enough that no goroutine ever blocks
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for _, lane := range lanes {
		wg.Add(1)
		go func(lane []int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, i := range lane {
				if ctx.Err() != nil {
					results <- finished{i, tools.Result{ID: calls[i].ID, Name: calls[i].Name, IsError: true,
						Output: "Not run: the request was stopped before this call started.", Short: "stopped"}}
					continue
				}
				results <- finished{i, run(i, calls[i])}
			}
		}(lane)
	}

	ready := make([]*tools.Result, n)
	next := 0
	for next < n {
		select {
		case <-ctx.Done():
			return false
		case f := <-results:
			r := f.r
			ready[f.i] = &r
			for next < n && ready[next] != nil {
				if !done(next, *ready[next]) {
					return false
				}
				next++
			}
		}
	}
	wg.Wait()
	return true
}

// execDuration is a small helper for logging how long a batch took.
func since(t time.Time) time.Duration { return time.Since(t).Round(time.Millisecond) }
