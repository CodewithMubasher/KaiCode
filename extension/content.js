// Runs in the DeepSeek tab (isolated world). It types Kai's prompt into the
// chat box, sends it, and forwards what inject.js captures to background.js.

let expectedPath = null; // the chat page Kai is working in
const waiters = new Map();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function relay(msg) {
  try {
    chrome.runtime.sendMessage(msg);
  } catch (_) {
    /* the extension was reloaded; this page needs a refresh */
  }
}

window.addEventListener('message', (e) => {
  if (e.source !== window || !e.data || e.data.kai !== true) return;
  const m = e.data;
  if (m.type === 'started') {
    const done = waiters.get(m.id);
    if (done) {
      waiters.delete(m.id);
      done(true);
    }
  } else if (m.type === 'chunk') {
    relay({ type: 'sse', id: m.id, data: m.data });
  } else if (m.type === 'end') {
    if (location.pathname.startsWith('/a/chat/s/')) expectedPath = location.pathname;
    relay({ type: 'sse_end', id: m.id });
  } else if (m.type === 'error') {
    relay({ type: 'sse_error', id: m.id, message: m.message });
  }
});

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg.type === 'ping') {
    sendResponse({ ok: true });
    return;
  }
  if (msg.type !== 'send') return;
  doSend(msg).then(sendResponse, (err) => sendResponse({ ok: false, error: String((err && err.message) || err) }));
  return true; // answer asynchronously
});

async function doSend({ id, prompt, newChat }) {
  if (!newChat && expectedPath && location.pathname !== expectedPath) {
    return { ok: false, code: 'chat_lost', error: 'the chat in the tab is not the one Kai started' };
  }

  const input = await waitFor(findInput, 20000);
  if (!input) return { ok: false, error: 'chat input box not found (is DeepSeek logged in?)' };

  window.postMessage({ kai: true, type: 'arm', id }, '*');
  const started = waitForStart(id, 4000);
  setText(input, prompt);
  await sleep(150);
  pressEnter(input);

  if (await started) return { ok: true };

  // Enter did not send: try the send button.
  const retry = waitForStart(id, 8000);
  if (!clickSend(input)) {
    window.postMessage({ kai: true, type: 'disarm' }, '*');
    return { ok: false, error: 'could not send the message (send button not found)' };
  }
  if (await retry) return { ok: true };

  window.postMessage({ kai: true, type: 'disarm' }, '*');
  return { ok: false, error: 'the page did not send the message' };
}

function waitForStart(id, ms) {
  return new Promise((resolve) => {
    waiters.set(id, resolve);
    setTimeout(() => {
      if (waiters.delete(id)) resolve(false);
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
