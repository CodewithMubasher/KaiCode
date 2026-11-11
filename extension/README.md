# Kai Bridge (Chrome extension)

Connects the Kai terminal app to your **DeepSeek** or **Qwen** web chat. Kai
types its prompts into a chat.deepseek.com or chat.qwen.ai tab and reads the
reply from the page's own stream, so the site's login, checks and cookies keep
working exactly as they do for you. Nothing leaves your machine except the
usual traffic to the chat you chose. Pick which one Kai uses in its provider
menu (Ctrl+P → Switch provider: *DeepSeek Web*, *Qwen Web*, or *Qwen API
(native tool calling)*). Qwen Web uses a strict JSON tool-call format; the
Qwen API option talks to DashScope's OpenAI-compatible endpoint and uses real
API tool calls.

## Set up (no token, no copy-paste)

1. `chrome://extensions` → turn on **Developer mode** → **Load unpacked** → pick this `extension` folder.
   (Updating from an older version? Click the reload icon on the extension card.)
2. Open https://chat.deepseek.com and/or https://chat.qwen.ai and log in. A tab that was already open is picked up automatically.
3. Run `kai`. The extension popup says *Connected to Kai*.

Set the page's DeepThink / Search toggles the way you want; Kai does not change them.

## How a Kai session maps to a web chat

- A new Kai session (start of `kai`, `/new`, `/clear`, ctrl+n) opens a **new chat** in the tab and sends Kai's instructions **once**.
- Every later message of that session goes into the **same chat**, with only the new text. DeepSeek keeps the history itself.
- If you wander off to another chat in that tab, Kai navigates back to its own chat before the next message.
- If the chat is gone (tab closed before it had an address), Kai starts a new chat and replays a short recap.

## If something does not work

- Click the extension icon → **Self-test**. It checks Kai, the tab, the page hook and the input box.
- The extension keeps no debug log (it slowed replies down). Kai's own log is `~/.kai/logs.txt`; `kai logs` prints its path and last lines.

## How it works

- `inject.js` (page context) wraps `fetch` (and XHR as a fallback); after Kai sends a message it copies the completion stream (`/api/v0/chat/completion` on DeepSeek, `/api/v2/chat/completions` on Qwen) and passes the raw SSE text on.
- `content.js` types the prompt into the chat box. On DeepSeek it presses Enter first (falls back to the send button); on Qwen it waits for the page to be idle, clicks the send button first, and retries with a fresh input element if the page dropped the text or did not start. Typing, Enter and the send-button click go through Chrome's debugger protocol, so the page sees **trusted** input; if the debugger cannot attach (for example while DevTools is open) `content.js` falls back to DOM events.
- `background.js` long-polls `http://127.0.0.1:8765/v1/poll` for commands and POSTs the SSE chunks and log lines back. The bridge only listens on localhost and rejects web pages.
- The extension never reads, stores or replays your DeepSeek token or cookies: the page makes its own requests with its own session. The `debugger` permission is used only for input into the chat tab.

## Known limits

- Selectors are heuristic (the chat is a `<textarea>`, Enter sends; Qwen: `textarea.message-input-textarea` and a send button labelled Send/send-button, and the page is idle when no stop button is showing). If a site changes its page, `content.js` is the only file to touch.
- A Qwen chat is recognised by its address `/c/<id>`. Kai keeps one chat per session and goes back to it if you wander off.
- A session belongs to one site: switching between DeepSeek Web and Qwen Web starts a new chat with a short recap.
- Trusted input needs the `debugger` permission and cannot attach while DevTools is open on the tab; the DOM fallback still works, but its events are synthetic and a page can tell them apart.
- Stopping a reply in Kai stops relaying it; the page may keep generating until it finishes.
