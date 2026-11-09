KaiCode - Opensource framework for converting any webchat ai into full local agent

## Providers

Kai starts on **DeepSeek Web** (the Chrome extension bridge). Press `ctrl+p` and
choose **Add provider** to use any OpenAI-compatible API instead (OpenAI,
OpenRouter, DeepSeek's official API, Groq, Ollama, LM Studio, vLLM, ...):

1. Name, Base URL (e.g. `https://api.openai.com/v1`), API key. The model is optional.
2. If you leave the model empty, Kai asks the provider for its model list and
   lets you pick one.
3. The provider is saved and activated; Kai remembers it for next time.

Switch with **Switch provider** (`/provider`), change model with **Switch model**
(`/model`). The conversation carries over when you switch.

## Storage

Everything is kept in one SQLite file, `~/.kai/kai.db` (`[storage] path` in
`config.toml`): custom providers (API keys are stored in plain text, the file is
readable by you only), sessions, every message, and settings such as the active
provider. **Switch session** (`/continue`), **Rename session** (`/rename`) and
**Delete session** are in the `ctrl+p` menu.

Build: run `go get modernc.org/sqlite && go mod tidy` once (pure Go, no C compiler).
