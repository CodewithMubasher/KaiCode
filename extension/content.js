// Runs in the DeepSeek or Qwen tab (isolated world). It types Kai's prompt into the
// chat box, sends it, and forwards what inject.js captures to background.js.
(() => {
  if (window.__kaiContentLoaded) return; // injected twice (manifest + scripting): run once
  window.__kaiContentLoaded = true;

  const waiters = new Map(); // request id -> resolve(true) when the page started the request
  let probeWaiter = null;

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  // The two supported chats differ only in these few details.
  const QWEN = location.hostname.includes('qwen');
  const HOST = location.hostname;
  const NAME = QWEN ? 'Qwen' : 'DeepSeek';
  const onLoginPage = () => (QWEN ? /\/auth|login|sign/ : /sign_in|login/).test(location.pathname);

  function relay(msg) {
    try {
      chrome.runtime.sendMessage(msg);
    } catch (_) {
      /* the extension was reloaded; this page needs a refresh */
    }
  }
  const log = (level, msg) => relay({ type: 'log', src: 'page', level, msg });

  // ---- trusted input --------------------------------------------------------
  // dispatchEvent() and el.click() produce events with isTrusted === false,
  // which a page can tell apart from a real user. background.js can drive
  // Chrome's own input pipeline through the debugger protocol; those events are
  // trusted. If that is unavailable (DevTools is open, the permission was not
  // granted, the tab is not inspectable), every helper falls back to the DOM
  // path so Kai keeps working.
  function trusted(op, data) {
    return new Promise((resolve) => {
      try {
        chrome.runtime.sendMessage({ type: 'trusted', op, ...data }, (res) => {
          if (chrome.runtime.lastError) {
            resolve({ ok: false, error: chrome.runtime.lastError.message });
          } else {
            resolve(res || { ok: false, error: 'no answer from the extension' });
          }
        });
      } catch (e) {
        resolve({ ok: false, error: String((e && e.message) || e) });
      }
    });
  }

  async function typePrompt(input, text) {
    input.focus();
    try {
      input.setSelectionRange(0, (input.value || '').length);
    } catch (_) {}
    const t = await trusted('insertText', { text });
    if (t.ok) {
      await sleep(150);
      if ((input.value || '').length === text.length) {
        log('info', `typed ${text.length} chars with trusted input`);
        return;
      }
      log('warn', `trusted input left ${(input.value || '').length} chars, expected ${text.length}: using the DOM fallback`);
    } else {
      log('warn', `trusted input unavailable (${t.error}): using the DOM fallback`);
    }
    setText(input, text);
    await sleep(250);
    if ((input.value || '').length !== text.length) {
      log('warn', `textarea holds ${(input.value || '').length} chars, expected ${text.length}: typing again`);
      setText(input, text);
      await sleep(250);
    }
  }

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
      case 'chat_id':
        relay({ type: 'chat_id', id: m.id, chatId: m.chatId, host: location.host });
        break;
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
      loggedOut: onLoginPage(),
    };
  }

  async function doSend({ id, prompt }) {
    log('info', `send ${id}: page ${location.pathname}, prompt ${prompt.length} chars`);

    if (onLoginPage()) {
      return { ok: false, error: `${HOST} is showing the login page: log in, then try again` };
    }

    let input = await waitFor(findInput, 30000);
    if (!input) {
      log('error', `no chat input on ${location.pathname} (title: ${document.title})`);
      return { ok: false, error: `the chat input box was not found (is ${NAME} logged in and fully loaded?)` };
    }

    window.postMessage({ kai: true, from: 'content', type: 'arm', id }, '*');
    await typePrompt(input, prompt);

    // Qwen is much more reliable with its send button than with Enter: the
    // button stays disabled while a reply is still being written, while Enter
    // silently does nothing in that state. Click first, retry with a fresh
    // element if the page replaced the box or dropped the text.
    if (QWEN) {
      if (await sendQwen(id, input, prompt)) return { ok: true };
      window.postMessage({ kai: true, from: 'content', type: 'disarm' }, '*');
      log('error', `Qwen never sent the message (textarea chars: ${(input.value || '').length})`);
      return { ok: false, error: 'the page did not send the message (see logs.txt for what the page did)' };
    }

    const started = waitForStart(id, 12000);
    await pressEnter(input);

    if (await started) return { ok: true };

    // Enter did not start a request within 12s.
    const left = (input.value || '').length;
    log('warn', `Enter did not send within 12s (textarea still has ${left} chars)`);
    if (left > 0) {
      const retry = waitForStart(id, 15000);
      const clicked = await clickSend(input);
      log('info', clicked ? 'clicked the send button' : 'no usable send button found');
      if (await retry) return { ok: true };
    } else {
      // The page took the text, so the request is probably still being prepared
      // (DeepSeek's proof-of-work). Give it more time.
      if (await waitForStart(id, 25000)) return { ok: true };
    }

    window.postMessage({ kai: true, from: 'content', type: 'disarm' }, '*');
    log('error', `the page never sent the message (textarea chars: ${(input.value || '').length})`);
    return { ok: false, error: 'the page did not send the message (see logs.txt for what the page did)' };
  }

  // sendQwen waits for the page to go idle, makes sure the box holds the
  // prompt, then clicks the send button. If no request starts it retries with
  // a fresh input element, because Qwen replaces the textarea when the chat
  // address changes.
  async function sendQwen(id, input, prompt) {
    for (let attempt = 0; attempt < 3; attempt++) {
      await waitIdle(attempt === 0 ? 20000 : 8000);
      input = await readyToSend(input, prompt, attempt === 0 ? 25000 : 8000);

      const started = waitForStart(id, 12000);
      const clicked = await clickSend(input);
      if (!clicked) {
        log('warn', 'Qwen has no enabled send button: pressing Enter');
        await pressEnter(input);
      }
      if (await started) {
        log('info', `Qwen started the request (attempt ${attempt + 1})`);
        return true;
      }

      log('warn', `Qwen did not start the request (attempt ${attempt + 1}/3, box has ${(input.value || '').length} of ${prompt.length} chars)`);
      await waitIdle(5000);
      const fresh = (await waitFor(findInput, 8000)) || input;
      input = fresh;
      if ((input.value || '').length !== prompt.length) await typePrompt(input, prompt);
    }
    return false;
  }

  // Qwen disables sending while a reply is still being written and exposes a
  // stop button during that time. Only a visible, enabled control counts: the
  // page may keep hidden buttons in the DOM.
  function shown(el) {
    if (!el || el.disabled || el.getAttribute('aria-disabled') === 'true') return false;
    if (el.offsetParent === null && getComputedStyle(el).position !== 'fixed') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }

  function pageBusy() {
    if (!QWEN) return false;
    const buttons = [...document.querySelectorAll('button, [role="button"]')].filter(shown);
    return buttons.some((b) => {
      const label = ((b.getAttribute('aria-label') || '') + ' ' + (b.title || '') + ' ' + (b.className || '')).toLowerCase();
      return /stop|停止/.test(label);
    });
  }

  async function waitIdle(ms) {
    return !!(await waitFor(() => !pageBusy(), ms));
  }

  // Qwen's send button is enabled only when the box has text AND the page is
  // idle (it is something else while a reply is still being written, and Enter
  // does nothing then). Typing a tool result right after a reply ends used to
  // leave the text sitting in the box. So wait for the button, and type again
  // if the page cleared the box meanwhile (it moves to the chat's own address
  // a moment after the reply).
  async function readyToSend(input, prompt, waitMs) {
    for (let attempt = 0; attempt < 2; attempt++) {
      const btn = await waitFor(() => findSendButton(input), waitMs);
      const box = (await waitFor(findInput, 3000)) || input;
      input = box;
      const have = (input.value || '').length;
      if (btn && have === prompt.length) {
        if (attempt > 0) log('info', 'the send button is ready');
        return input;
      }
      log('warn', `Qwen is not ready to send (button ${btn ? 'enabled' : 'disabled'}, box has ${have} of ${prompt.length} chars): typing again`);
      await typePrompt(input, prompt);
      waitMs = 8000;
    }
    log('warn', 'Qwen never showed an enabled send button: trying Enter anyway');
    return input;
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
    if (QWEN) {
      const q = document.querySelector('textarea.message-input-textarea');
      if (q && !q.disabled && !q.readOnly) return q;
    }
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

  async function pressEnter(input) {
    const t = await trusted('key', { key: 'Enter' });
    if (t.ok) return;
    log('warn', `trusted Enter unavailable (${t.error}): using DOM events`);
    syntheticEnter(input);
  }

  function syntheticEnter(el) {
    const init = { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true };
    el.dispatchEvent(new KeyboardEvent('keydown', init));
    el.dispatchEvent(new KeyboardEvent('keypress', init));
    el.dispatchEvent(new KeyboardEvent('keyup', init));
  }

  // The send button is an icon-only button near the input.
  function findSendButton(input) {
    if (QWEN) {
      // <button class="send-button" aria-label="Send">; the same element turns
      // into a stop button while a reply is being written, so exclude those.
      const all = [...document.querySelectorAll('button, [role="button"]')].filter(shown);
      const send = all.find((b) => {
        const label = ((b.getAttribute('aria-label') || '') + ' ' + (b.title || '') + ' ' + (b.className || '')).toLowerCase();
        if (/stop|停止/.test(label)) return false;
        return b.classList.contains('send-button') || /send|发送/.test(label);
      });
      if (send) return send;
    }
    let box = input;
    for (let i = 0; i < 5 && box.parentElement; i++) box = box.parentElement;
    const buttons = [...box.querySelectorAll('[role="button"], button')].filter(
      (b) =>
        !b.disabled &&
        b.getAttribute('aria-disabled') !== 'true' &&
        b.querySelector('svg') &&
        !(b.textContent || '').trim()
    );
    return buttons[buttons.length - 1] || null;
  }

  async function clickSend(input) {
    const send = findSendButton(input);
    if (!send) return false;
    send.scrollIntoView({ block: 'center', inline: 'center' });
    await sleep(100);
    const r = send.getBoundingClientRect();
    const t = await trusted('click', { x: r.left + r.width / 2, y: r.top + r.height / 2 });
    if (t.ok) {
      log('info', 'clicked the send button with trusted input');
      return true;
    }
    log('warn', `trusted click unavailable (${t.error}): using the DOM fallback`);
    send.click();
    return true;
  }
})();
