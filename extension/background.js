// Service worker: long-polls Kai for commands, drives the DeepSeek tab, and
// forwards the page's raw SSE back to Kai.

const DEFAULT_ADDR = 'http://127.0.0.1:8765';
const FRESH_CHAT = 'https://chat.deepseek.com/';

let connected = false;
let lastError = '';
let polling = false;
let lastTabId = null;
const cancelled = new Set();
const outbox = new Map();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function settings() {
  const s = await chrome.storage.local.get(['token', 'addr']);
  return { token: (s.token || '').trim(), addr: (s.addr || DEFAULT_ADDR).replace(/\/+$/, '') };
}

function setState(ok, error) {
  connected = ok;
  lastError = error || '';
  chrome.action.setBadgeText({ text: ok ? 'ON' : '' });
  chrome.action.setBadgeBackgroundColor({ color: '#1a7f5a' });
}

// ---- talking to Kai ------------------------------------------------------

async function pollLoop() {
  if (polling) return;
  polling = true;
  try {
    for (;;) {
      const { token, addr } = await settings();
      if (!token) {
        setState(false, 'no token set');
        await sleep(2000);
        continue;
      }
      try {
        const r = await fetch(`${addr}/v1/poll`, { headers: { 'X-Kai-Token': token } });
        if (r.status === 200) {
          setState(true);
          handleCommand(await r.json());
        } else if (r.status === 204) {
          setState(true);
        } else {
          setState(false, r.status === 401 ? 'wrong token' : `Kai answered ${r.status}`);
          await sleep(3000);
        }
      } catch (_) {
        setState(false, 'Kai is not running');
        await sleep(2000);
      }
    }
  } finally {
    polling = false;
  }
}

async function post(ev) {
  const { token, addr } = await settings();
  try {
    const r = await fetch(`${addr}/v1/event`, {
      method: 'POST',
      headers: { 'X-Kai-Token': token, 'Content-Type': 'application/json' },
      body: JSON.stringify(ev),
    });
    if (!r.ok) lastError = `Kai answered ${r.status}`;
  } catch (e) {
    lastError = String(e);
  }
}

// Chunks are sent in order; while one is in flight, newer ones are merged.
function enqueue(id, part) {
  if (cancelled.has(id)) return;
  let o = outbox.get(id);
  if (!o) {
    o = { id, buf: '', final: null, busy: false };
    outbox.set(id, o);
  }
  if (part.chunk) o.buf += part.chunk;
  if (part.end) o.final = { id, type: 'end' };
  if (part.error) o.final = { id, type: 'error', message: part.error };
  flush(o);
}

async function flush(o) {
  if (o.busy) return;
  o.busy = true;
  try {
    while (o.buf || o.final) {
      if (o.buf) {
        const data = o.buf;
        o.buf = '';
        await post({ id: o.id, type: 'chunk', data });
      } else {
        const final = o.final;
        o.final = null;
        await post(final);
        outbox.delete(o.id);
      }
    }
  } finally {
    o.busy = false;
  }
}

// ---- commands from Kai ---------------------------------------------------

function handleCommand(cmd) {
  if (cmd.type === 'cancel') {
    cancelled.add(cmd.id);
    outbox.delete(cmd.id);
    if (cancelled.size > 50) cancelled.delete(cancelled.values().next().value);
  } else if (cmd.type === 'send') {
    runSend(cmd);
  }
}

async function runSend(cmd) {
  try {
    const tab = await ensureTab(cmd.newChat);
    const res = await sendToTab(tab.id, { type: 'send', id: cmd.id, prompt: cmd.prompt, newChat: cmd.newChat });
    if (!res || !res.ok) {
      await post({
        id: cmd.id,
        type: res && res.code === 'chat_lost' ? 'chat_lost' : 'error',
        message: (res && res.error) || 'the DeepSeek page did not respond',
      });
    }
  } catch (e) {
    await post({ id: cmd.id, type: 'error', message: String((e && e.message) || e) });
  }
}

async function ensureTab(newChat) {
  const tabs = await chrome.tabs.query({ url: 'https://chat.deepseek.com/*' });
  let tab = tabs.find((t) => t.id === lastTabId) || tabs[0];
  if (!tab) {
    tab = await chrome.tabs.create({ url: FRESH_CHAT, active: false });
    await waitLoaded(tab.id);
  } else if (newChat && new URL(tab.url).pathname !== '/') {
    const loaded = waitLoaded(tab.id);
    await chrome.tabs.update(tab.id, { url: FRESH_CHAT });
    await loaded;
  }
  lastTabId = tab.id;
  return tab;
}

function waitLoaded(tabId, timeout = 20000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      chrome.tabs.onUpdated.removeListener(listener);
      reject(new Error('the DeepSeek page took too long to load'));
    }, timeout);
    function listener(id, info) {
      if (id === tabId && info.status === 'complete') {
        clearTimeout(timer);
        chrome.tabs.onUpdated.removeListener(listener);
        resolve();
      }
    }
    chrome.tabs.onUpdated.addListener(listener);
  });
}

// The content script may not be up yet right after a page load.
async function sendToTab(tabId, msg) {
  for (let i = 0; i < 20; i++) {
    try {
      return await chrome.tabs.sendMessage(tabId, msg);
    } catch (_) {
      await sleep(500);
    }
  }
  throw new Error('could not reach the DeepSeek page: reload the tab and try again');
}

// ---- messages from the page and the popup --------------------------------

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  switch (msg.type) {
    case 'status':
      sendResponse({ connected, lastError });
      return;
    case 'poll_now':
      pollLoop();
      sendResponse({});
      return;
    case 'sse':
      enqueue(msg.id, { chunk: msg.data });
      return;
    case 'sse_end':
      enqueue(msg.id, { end: true });
      return;
    case 'sse_error':
      enqueue(msg.id, { error: msg.message });
      return;
  }
});

// ---- keep the worker alive -----------------------------------------------

chrome.alarms.create('kai-wake', { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener(() => pollLoop());
setInterval(() => chrome.runtime.getPlatformInfo(() => {}), 20000);
chrome.runtime.onStartup.addListener(() => pollLoop());
chrome.runtime.onInstalled.addListener(() => pollLoop());
chrome.storage.onChanged.addListener(() => pollLoop());

pollLoop();
