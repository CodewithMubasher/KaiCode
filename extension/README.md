# Kai Bridge (Chrome extension)

Connects the Kai terminal app to your DeepSeek web chat. Kai types its prompts
into a chat.deepseek.com tab and reads the reply from the page's own stream,
so DeepSeek's login, proof-of-work and cookies keep working exactly as they do
for you. Nothing leaves your machine except the usual traffic to DeepSeek.

## Set up

1. `chrome://extensions` → turn on **Developer mode** → **Load unpacked** → pick this `extension` folder.
2. In a terminal run `kai token`. It prints the address and a secret token.
3. Click the extension icon, paste both, **Save**. It should say *Connected to Kai* once `kai` is running.
4. Open https://chat.deepseek.com and log in. Reload the tab if it was open before you installed the extension.
5. Set the page's DeepThink / Search toggles the way you want; Kai does not change them.
6. Run `kai` and send a message.

## How it works

- `inject.js` (page context) wraps `fetch`; after Kai sends a message it copies the `/api/v0/chat/completion` stream and passes the raw SSE text on.
- `content.js` types the prompt into the chat box and presses Enter (falls back to the send button).
- `background.js` long-polls `http://127.0.0.1:8765/v1/poll` for commands and POSTs the SSE chunks back to `/v1/event`. Every request carries your token.

## Known limits

- Selectors are heuristic (the chat is a `<textarea>`, Enter sends). If DeepSeek changes its page, `content.js` is the only file to touch.
- Stopping a reply in Kai stops relaying it; the page may keep generating until it finishes.
- A "new chat" navigates the Kai tab to chat.deepseek.com. Do not use that tab while Kai is working.
