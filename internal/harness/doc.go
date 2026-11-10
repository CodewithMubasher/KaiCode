// Package harness is Kai's agent loop: it sends the conversation to a provider,
// reads the streamed reply, finds tool calls in it, runs them, sends the
// results back, and repeats until the model answers without calling a tool.
//
// There are two paths, chosen by the backend, and they share no prompt, parser
// or message format:
//
//   - DSML (agent_web.go, package dsml): a Provider such as DeepSeek Web, a web
//     chat with no system role and no native tools. The tool list and the
//     DSML call format are written into the first message, and tool calls are
//     read out of the reply text.
//
//   - Native (agent_native.go): a NativeProvider such as an OpenAI-compatible
//     API. Instructions are a system message, tools are API definitions, calls
//     come back as structured tool_calls and results go back as tool messages.
//     The API is stateless, so the agent keeps the conversation.
package harness
