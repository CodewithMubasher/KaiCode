// Runs inside the DeepSeek or Qwen page (MAIN world). It wraps fetch (and XHR as a
// fallback) so that, when Kai has just sent a message, the page's own
// completion response is copied and its raw SSE text is handed to content.js.
// The page itself sees the response untouched: it still does the
// proof-of-work, the headers and the cookies.
(() => {
  const VERSION = 5;
  const SITE_NAME = location.hostname.includes('qwen') ? 'Qwen' : 'DeepSeek';
  // A newer copy of this script (after an extension update) takes over; the
  // older one goes quiet instead of capturing everything twice.
  const token = Math.random().toString(36).slice(2);
  window.__kaiActive = token;
  const alive = () => window.__kaiActive === token;

  const post = (m) => window.postMessage({ kai: true, from: 'inject', ...m }, '*');
  let armedId = null; // the Kai request waiting for its completion call
  let armTimer = null;
  let seenCompletions = 0;

  const pathOf = (url) => {
    try {
      return new URL(url, location.href).pathname;
    } catch (_) {
      return String(url);
    }
  };
  // DeepSeek: /api/v0/chat/completion   Qwen: /api/v2/chat/completions
  const isCompletionPath = (p) => p.includes('/chat/completion');

  window.addEventListener('message', (e) => {
    if (!alive() || e.source !== window || !e.data || e.data.kai !== true || e.data.from === 'inject') return;
    const m = e.data;
    if (m.type === 'arm') {
      armedId = m.id;
      clearTimeout(armTimer);
      armTimer = setTimeout(() => {
        if (armedId === m.id) {
          armedId = null;
        }
      }, 180000);
    } else if (m.type === 'disarm') {
      armedId = null;
      clearTimeout(armTimer);
    } else if (m.type === 'probe') {
      post({ type: 'probe_ack', version: VERSION, seen: seenCompletions, fetchIsOurs: window.fetch === ours });
    }
  });

  // ---- fetch ---------------------------------------------------------------
  const original = window.fetch;

  window.fetch = function (input, init) {
    if (!alive()) return original.apply(this, arguments);

    let url = '';
    let method = 'GET';
    try {
      url = typeof input === 'string' ? input : (input && (input.url || input.href)) || String(input);
      method = String((init && init.method) || (input && input.method) || 'GET').toUpperCase();
    } catch (_) {}
    const path = pathOf(url);
    const armed = armedId;
    const completion = method === 'POST' && isCompletionPath(path);

    if (completion) seenCompletions++;
    if (armed && completion) {
      armedId = null;
      clearTimeout(armTimer);
      post({ type: 'sent', id: armed });
      // Qwen names the chat in the request address (?chat_id=...). It is known
      // before the page moves to the chat's own URL, so Kai can find the chat again.
      try {
        const cid = new URL(url, location.href).searchParams.get('chat_id');
        if (cid) post({ type: 'chat_id', id: armed, chatId: cid });
      } catch (_) {}
    }

    const p = original.apply(this, arguments);
    if (!armed) return p;

    return p.then(
      (res) => {
        try {
          let id = completion ? armed : null;
          if (!id && armedId === armed) {
            // The completion endpoint was renamed: take the event stream anyway.
            const ct = String((res.headers && res.headers.get('content-type')) || '');
            if (ct.includes('text/event-stream')) {
              id = armed;
              armedId = null;
              clearTimeout(armTimer);
              post({ type: 'sent', id });
            }
          }
          if (id) {
            if (!res.ok || !res.body) {
              post({ type: 'error', id, message: `${SITE_NAME} answered HTTP ${res.status}` });
            } else {
              relay(id, res.clone());
            }
          }
        } catch (_) {
          // the page must never see a failure caused by the hook
        }
        return res;
      },
      (err) => {
        if (armed && completion) post({ type: 'error', id: armed, message: `Request failed: ${err}` });
        throw err;
      }
    );
  };

  const ours = window.fetch;

  async function relay(id, res) {
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        const data = decoder.decode(value, { stream: true });
        if (data) {
          post({ type: 'chunk', id, data });
        }
      }
      const tail = decoder.decode();
      if (tail) post({ type: 'chunk', id, data: tail });
      post({ type: 'end', id });
    } catch (err) {
      post({ type: 'error', id, message: `Reading reply failed` });
    }
  }

  // ---- XMLHttpRequest fallback (in case the page does not use fetch) ----------
  try {
    const XHR = XMLHttpRequest.prototype;
    const open = XHR.open;
    const send = XHR.send;
    XHR.open = function (method, url) {
      this.__kai = { method: String(method).toUpperCase(), path: pathOf(url) };
      return open.apply(this, arguments);
    };
    XHR.send = function () {
      const k = this.__kai;
      if (alive() && k && armedId && k.method === 'POST' && isCompletionPath(k.path)) {
        const id = armedId;
        armedId = null;
        clearTimeout(armTimer);
        post({ type: 'sent', id });
        let offset = 0;
        const pump = () => {
          try {
            const text = this.responseText || '';
            if (text.length > offset) {
              post({ type: 'chunk', id, data: text.slice(offset) });
              offset = text.length;
            }
          } catch (_) {}
        };
        this.addEventListener('progress', pump);
        this.addEventListener('loadend', () => {
          pump();
          if (this.status >= 200 && this.status < 300) post({ type: 'end', id });
          else post({ type: 'error', id, message: `${SITE_NAME} answered HTTP ${this.status}` });
        });
      }
      return send.apply(this, arguments);
    };
  } catch (err) {
    // Silent fail for XHR hook
  }

  // Hook installed silently
})();
