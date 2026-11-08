package harness

import "kai/internal/tools"

// EventKind is the type of an Event.
type EventKind int

const (
	EvThinking   EventKind = iota // reasoning delta (Text)
	EvText                        // visible reply delta (Text)
	EvToolCall                    // the model called a tool (Call), about to run
	EvToolResult                  // a tool finished (Call and Result)
	EvDone                        // the request is finished
	EvError                       // the request failed (Err)
)

// Event is what the harness reports to the UI while it works.
type Event struct {
	Kind   EventKind
	Text   string
	Call   *tools.Call
	Result *tools.Result
	Err    error
}
