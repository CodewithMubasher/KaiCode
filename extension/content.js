// Runs in the DeepSeek tab (isolated world). It types Kai's prompt into the
// chat box, sends it, and forwards what inject.js captures to background.js.
(() => {
  if (window.__kaiContentLoaded) return; // injected twice (manifest + scripting): run once
  window.__kaiContentLoaded = true;

  const waiters = new Map(); // request id -> resolve(true) when the page started the request
  let probeWaiter = null;

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  function relay(msg) {
    try {
      chrome.runtime.sendMessage(msg);
    } catch (_) {
      /* the extension was reloaded; this page needs a refresh */
    }
  }
  const log = (level, msg) => relay({ type: 'log', src: 'page', level, msg });

  // ---- what the page-world hook tells us ------------------------------------
  window.addEventListener('message', (e) => {
    if (e.source !== window || !e.data || e.data.kai !== true || e.data.from !== 'inject') return;
    const m = e.data;
    switch (m.type) {
      case 'sent': {
        const done = waiters.get(m.id);
        if (done) {
          waiters.delete(m.id);
          done(true);
        }
        relay({ type: 'sent', id: m.id });
        break;
      }
      case 'chunk':
        relay({ type: 'sse', id: m.id, data: m.data });
        break;
      case 'end':
        relay({ type: 'page_url', url: location.href });
        relay({ type: 'sse_end', id: m.id });
        break;
      case 'error':
        relay({ type: 'sse_error', id: m.id, message: m.message });
        break;
      case 'log':
        relay({ type: 'log', src: 'inject', level: m.level, msg: m.msg });
        break;
      case 'probe_ack':
        if (probeWaiter) {
          probeWaiter(m);
          probeWaiter = null;
        }
        break;
    }
  });

  // ---- commands from background.js ------------------------------------------
  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    if (msg.type === 'ping') {
      sendResponse({ ok: true, path: location.pathname });
      return;
    }
    if (msg.type === 'probe') {
      probe().then(sendResponse);
      return true;
    }
    if (msg.type !== 'send') return;
    doSend(msg).then(sendResponse, (err) => {
      log('error', `send crashed: ${(err && err.stack) || err}`);
      sendResponse({ ok: false, error: String((err && err.message) || err) });
    });
    return true; // answer asynchronously
  });

  async function probe() {
    const hook = await new Promise((resolve) => {
      probeWaiter = resolve;
      window.postMessage({ kai: true, from: 'content', type: 'probe' }, '*');
      setTimeout(() => {
        if (probeWaiter === resolve) {
          probeWaiter = null;
          resolve(null);
        }
      }, 1500);
    });
    return {
      ok: true,
      path: location.pathname,
      hooked: !!hook,
      hook,
      hasInput: !!findInput(),
      loggedOut: /sign_in|login/.test(location.pathname),
    };
  }

  async function doSend({ id, prompt }) {
    log('info', `send ${id}: page ${location.pathname}, prompt ${prompt.length} chars`);

    if (/sign_in|login/.test(location.pathname)) {
      return { ok: false, error: 'chat.deepseek.com is showing the login page: log in, then try again' };
    }

    const input = await waitFor(findInput, 30000);
    if (!input) {
      log('error', `no chat input on ${location.pathname} (title: ${document.title})`);
      return { ok: false, error: 'the chat input box was not found (is DeepSeek logged in and fully loaded?)' };
    }

    window.postMessage({ kai: true, from: 'content', type: 'arm', id }, '*');
    const started = waitForStart(id, 12000);
    setText(input, prompt);
    await sleep(250);
    if ((input.value || '').length !== prompt.length) {
      log('warn', `textarea holds ${(input.value || '').length} chars, expected ${prompt.length}: typing again`);
      setText(input, prompt);
      await sleep(250);
    }
    pressEnter(input);

    if (await started) return { ok: true };

    // Enter did not start a request within 12s.
    const left = (input.value || '').length;
    log('warn', `Enter did not send within 12s (textarea still has ${left} chars)`);
    if (left > 0) {
      const retry = waitForStart(id, 15000);
      const clicked = clickSend(input);
      log('info', clicked ? 'clicked the send button' : 'no usable send button found');
      if (await retry) return { ok: true };
    } else {
      // The page took the text, so the request is probably waiting on DeepSeek's
      // proof-of-work. Give it more time.
      if (await waitForStart(id, 25000)) return { ok: true };
    }

    window.postMessage({ kai: true, from: 'content', type: 'disarm' }, '*');
    log('error', `the page never sent the message (textarea chars: ${(input.value || '').length})`);
    return { ok: false, error: 'the page did not send the message (see logs.txt for what the page did)' };
  }

  function waitForStart(id, ms) {
    return new Promise((resolve) => {
      waiters.set(id, resolve);
      setTimeout(() => {
        if (waiters.get(id) === resolve) {
          waiters.delete(id);
          resolve(false);
        }
      }, ms);
    });
  }

  async function waitFor(fn, ms) {
    const end = Date.now() + ms;
    while (Date.now() < end) {
      const v = fn();
      if (v) return v;
      await sleep(250);
    }
    return null;
  }

  function findInput() {
    const byId = document.querySelector('textarea#chat-input');
    if (byId && !byId.disabled && !byId.readOnly) return byId;
    const boxes = [...document.querySelectorAll('textarea')].filter(
      (el) => el.offsetParent !== null && !el.disabled && !el.readOnly
    );
    return boxes[boxes.length - 1] || null;
  }

  // React ignores a plain `el.value = ...`; go through the native setter.
  function setText(el, text) {
    el.focus();
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
    set.call(el, text);
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function pressEnter(el) {
    const init = { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true };
    el.dispatchEvent(new KeyboardEvent('keydown', init));
    el.dispatchEvent(new KeyboardEvent('keypress', init));
    el.dispatchEvent(new KeyboardEvent('keyup', init));
  }

  // The send button is an icon-only button near the input.
  function clickSend(input) {
    let box = input;
    for (let i = 0; i < 5 && box.parentElement; i++) box = box.parentElement;
    const buttons = [...box.querySelectorAll('[role="button"], button')].filter(
      (b) =>
        !b.disabled &&
        b.getAttribute('aria-disabled') !== 'true' &&
        b.querySelector('svg') &&
        !(b.textContent || '').trim()
    );
    const send = buttons[buttons.length - 1];
    if (!send) return false;
    send.click();
    return true;
  }
})();
