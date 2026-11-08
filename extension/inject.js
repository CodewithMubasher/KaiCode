// Runs inside the DeepSeek page (MAIN world). It wraps fetch so that, when Kai
// has just sent a message, the page's own completion response is copied and
// its raw SSE text is handed to content.js. The page itself sees the response
// untouched: it still does the proof-of-work, the headers and the cookies.
(() => {
  if (window.__kaiHooked) return;
  window.__kaiHooked = true;

  const originalFetch = window.fetch;
  let armedId = null; // the Kai request waiting for its completion call

  const post = (m) => window.postMessage({ kai: true, ...m }, '*');

  window.addEventListener('message', (e) => {
    if (e.source !== window || !e.data || e.data.kai !== true) return;
    if (e.data.type === 'arm') armedId = e.data.id;
    else if (e.data.type === 'disarm') armedId = null;
  });

  window.fetch = async function (input, init) {
    const url = typeof input === 'string' ? input : (input && input.url) || '';
    const res = await originalFetch.apply(this, arguments);
    if (!armedId || !url.includes('/api/v0/chat/completion')) return res;

    const id = armedId;
    armedId = null;
    post({ type: 'started', id });

    if (!res.ok || !res.body) {
      post({ type: 'error', id, message: `DeepSeek answered HTTP ${res.status}` });
      return res;
    }
    relay(id, res.clone());
    return res;
  };

  async function relay(id, res) {
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        const data = decoder.decode(value, { stream: true });
        if (data) post({ type: 'chunk', id, data });
      }
      const tail = decoder.decode();
      if (tail) post({ type: 'chunk', id, data: tail });
      post({ type: 'end', id });
    } catch (err) {
      post({ type: 'error', id, message: String(err) });
    }
  }
})();
