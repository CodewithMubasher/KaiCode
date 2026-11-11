package harness

import (
	"kai/internal/harness/dsml"
	"kai/internal/harness/jsoncall"
	"kai/internal/tools"
)

// ToolProtocol is the text format a web chat uses to ask for tools. Native
// (OpenAI-compatible) providers do not use it: they get structured schemas and
// answer with structured tool_calls.
type ToolProtocol int

const (
	// ProtocolDSML is DeepSeek's <|DSML|tool_calls> markup.
	ProtocolDSML ToolProtocol = iota
	// ProtocolJSON is Kai's strict <tool_call>{...}</tool_call> protocol,
	// used by Qwen Web. The model cannot send real API tool_calls through the
	// page, so the prompt and the parser enforce this format instead.
	ProtocolJSON
)

// Protocoler is implemented by web-chat providers that use a protocol other
// than DSML.
type Protocoler interface {
	ToolProtocol() ToolProtocol
}

// ProtocolOf reports the protocol a provider speaks. Providers that do not
// say otherwise use DSML, which is the original web-chat format.
func ProtocolOf(p Provider) ToolProtocol {
	if pr, ok := p.(Protocoler); ok {
		return pr.ToolProtocol()
	}
	return ProtocolDSML
}

// replyParser is the streaming parser of one protocol: it releases visible
// text, hides markup, and collects tool calls.
type replyParser interface {
	Feed(string) string
	Finish() string
	Calls() []tools.Call
	Warnings() []string
	Progress() (ToolProgress, bool)
}

func newReplyParser(proto ToolProtocol) replyParser {
	if proto == ProtocolJSON {
		return jsonReply{p: jsoncall.NewParser()}
	}
	return dsmlReply{p: dsml.NewParser()}
}

type dsmlReply struct{ p *dsml.Parser }

func (r dsmlReply) Feed(s string) string { return r.p.Feed(s) }
func (r dsmlReply) Finish() string       { return r.p.Finish() }
func (r dsmlReply) Calls() []tools.Call  { return r.p.Calls() }
func (r dsmlReply) Warnings() []string   { return r.p.Warnings() }
func (r dsmlReply) Progress() (ToolProgress, bool) {
	tp, ok := r.p.Progress()
	if !ok {
		return ToolProgress{}, false
	}
	return ToolProgress{Name: tp.Name, Path: tp.Path, Lines: tp.Lines, Creating: tp.Creating}, true
}

type jsonReply struct{ p *jsoncall.Parser }

func (r jsonReply) Feed(s string) string { return r.p.Feed(s) }
func (r jsonReply) Finish() string       { return r.p.Finish() }
func (r jsonReply) Calls() []tools.Call  { return r.p.Calls() }
func (r jsonReply) Warnings() []string   { return r.p.Warnings() }
func (r jsonReply) Progress() (ToolProgress, bool) {
	// The JSON block is one value; the call is reported when it closes. The
	// UI still shows the reason sentence before the tool row.
	return ToolProgress{}, false
}

// firstPrompt builds the first message of a web-chat session for a protocol.
func firstPrompt(proto ToolProtocol, system string, specs []tools.Spec, user string) string {
	if proto == ProtocolJSON {
		return jsoncall.FirstPrompt(system, specs, user)
	}
	return dsml.FirstPrompt(system, specs, user)
}

// repairPrompt asks the model to resend an unreadable tool call.
func repairPrompt(proto ToolProtocol, reason string) string {
	if proto == ProtocolJSON {
		return jsoncall.RepairPrompt(reason)
	}
	return dsml.RepairPrompt(reason)
}

// encodeResults builds the message that carries tool results back. The tool
// manifest is repeated so the model cannot forget which tools exist.
func encodeResults(proto ToolProtocol, specs []tools.Spec, rs []tools.Result) string {
	if proto == ProtocolJSON {
		return jsoncall.EncodeResults(rs, specs)
	}
	return dsml.ResultsPrompt(rs, specs)
}
