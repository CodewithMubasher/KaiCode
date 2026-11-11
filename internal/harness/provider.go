package harness

import (
	"context"
	"errors"

	"kai/internal/tools"
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
	StreamToolCall                       // a complete native tool call (native providers only); ToolCall holds it
	StreamUsage                          // the provider reported its real token usage (Usage)
)

// Usage is the token usage a provider reports for one request. It is the
// provider's own count, not an estimate: prompt tokens are everything the
// model read (instructions, history and tool definitions included) and
// completion tokens are everything it wrote.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Total returns the count to show for the context: the total when the provider
// gives one, otherwise prompt plus completion.
func (u Usage) Total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

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
	ToolCall  *ToolCall     // StreamToolCall
	Usage     *Usage        // StreamUsage
	Err       error
}

// Provider is the DSML path: a web chat that has no system role and no native
// tools. The agent sends it text prompts and finds tool calls in the reply
// with the dsml parser. Only DeepSeek Web uses it.
//
// Provider sends a Turn and streams the reply back. The returned channel must
// be closed when the reply is over or ctx is cancelled.
type Provider interface {
	Send(ctx context.Context, t Turn) (<-chan StreamEvent, error)
}

// Sessioner is implemented by providers that can start a fresh chat.
type Sessioner interface {
	NewSession(ctx context.Context) error
}

// ---- native tool calling ---------------------------------------------------
//
// NativeProvider is the other path, used by OpenAI-compatible APIs. It shares
// nothing with the DSML path: the API gets a real system message, the tools as
// structured definitions, and answers with structured tool calls, so there is
// no DSML in the prompt, no DSML parser on the reply, and the results go back
// as "tool" messages instead of <tool_result> text.

// ToolCall is one tool call the model made through the API. Arguments is the
// JSON text exactly as the model wrote it; the agent parses it.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatMessage is one message of an API conversation.
type ChatMessage struct {
	Role       string     // "system", "user", "assistant" or "tool"
	Content    string     //
	Reasoning  string     // assistant: the model's reasoning, echoed back only while a request is running
	ToolCalls  []ToolCall // assistant: the calls it made
	ToolCallID string     // tool: the call this message answers
}

// ChatRequest is the whole conversation. Native providers are stateless: the
// agent owns the history and sends all of it every time.
type ChatRequest struct {
	Messages []ChatMessage // the first one is the system message
	Tools    []tools.Spec
}

// NativeProvider streams the reply to a ChatRequest. Tool calls arrive as
// StreamToolCall events (after the reply text), never as text. The channel
// must be closed when the reply is over or ctx is cancelled.
type NativeProvider interface {
	Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// Backend is what an Agent talks to: exactly one of the two paths.
type Backend struct {
	Web    Provider       // DSML text protocol (DeepSeek Web)
	Native NativeProvider // API tool calling (custom OpenAI-compatible providers)
}
