package harness

import (
	"context"
	"errors"
)

// ErrChatLost means the chat on the provider side is gone or is no longer the
// one Kai started (the user opened another chat, the page was reloaded). The
// agent recovers by starting a fresh chat and replaying a recap of its own
// record of the conversation.
var ErrChatLost = errors.New("the web chat was lost")

// Turn is one message sent to the model.
type Turn struct {
	Prompt   string
	ParentID *int   // id of the model's previous reply; nil for the first message of a chat
	Thinking bool   // ask for extended reasoning
	Session  string // identifies the TUI session; one session = one web chat
	NewChat  bool   // true for the first message of a session: open a fresh web chat
}

// StreamKind is the type of a StreamEvent.
type StreamKind int

const (
	StreamReady        StreamKind = iota // the reply started; MessageID is its id
	StreamThinking                       // reasoning text delta
	StreamText                           // reply text delta
	StreamTitle                          // the chat got a title
	StreamEnd                            // the reply is complete; Text is the status ("FINISHED")
	StreamError                          // the stream failed; Err says why
	StreamSent                           // the prompt reached the web chat (the chat now exists)
	StreamToolProgress                   // a tool call is still being written; Tool says which
)

// ToolProgress is a tool call as it is being written: the model has sent the
// invoke and part of its arguments but not the whole block yet.
type ToolProgress struct {
	Name     string // tool name, e.g. "edit_file"
	Path     string // the path argument, once it has fully arrived
	Lines    int    // lines of new content written so far
	Creating bool   // no old_string: a new file
}

// StreamEvent is one piece of a streamed model reply.
type StreamEvent struct {
	Kind      StreamKind
	Text      string
	MessageID int
	Tool      *ToolProgress // StreamToolProgress
	Err       error
}

// Provider sends a Turn and streams the reply back. The returned channel must
// be closed when the reply is over or ctx is cancelled.
type Provider interface {
	Send(ctx context.Context, t Turn) (<-chan StreamEvent, error)
}

// Sessioner is implemented by providers that can start a fresh chat.
type Sessioner interface {
	NewSession(ctx context.Context) error
}
