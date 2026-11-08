// Package bridge connects Kai to the Chrome extension. The extension runs in
// the browser where the DeepSeek login lives; it types Kai's prompts into the
// web chat and relays the page's raw SSE stream back here, where the DeepSeek
// decoder turns it into harness events. Server is a harness.Provider.
//
// The transport is plain HTTP on 127.0.0.1, so it needs nothing outside the
// standard library:
//
//	GET  /v1/poll    long-poll: the extension asks for its next command
//	POST /v1/event   the extension reports raw SSE chunks, the end, or an error
//	GET  /v1/status  is an extension polling?
//
// Every request must carry the shared token in X-Kai-Token, and a browser
// Origin, if present, must be chrome-extension://.
package bridge
