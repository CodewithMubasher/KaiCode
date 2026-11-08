// Package harness is Kai's agent loop: it sends a prompt to a Provider
// (DeepSeek web chat through the bridge, or a mock), reads the streamed reply,
// finds tool calls in it, runs them, sends the results back, and repeats
// until the model answers without calling a tool.
package harness
