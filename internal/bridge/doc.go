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
//	POST /v1/log     the extension's own log lines, merged into logs.txt
//	GET  /v1/status  is an extension polling?
//
// There is no token to configure. The server only answers on localhost, a
// browser Origin, if present, must be chrome-extension://, and every request
// must carry the X-Kai-Client header (which web pages cannot add).
package bridge
