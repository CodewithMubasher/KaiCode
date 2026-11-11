// Service worker: long-polls Kai for commands, drives the DeepSeek or Qwen tab, and
// forwards the page's raw SSE back to Kai. No setup: the address is fixed and
// there is no token.

const DEFAULT_ADDR = 'http://127.0.0.1:8765';
const HEADERS = { 'X-Kai-Client': 'kai-bridge' };

// The web chats Kai can drive. Kai says which one in every command.
const SITES = {
  deepseek: {
    name: 'DeepSeek',
    host: 'chat.deepseek.com',
    fresh: 'https://chat.deepseek.com/',
    match: 'https://chat.deepseek.com/*',
    isChatPath: (p) => p !== '/' && p !== '' && !/sign_in|sign_up|login|register/.test(p),
  },
  qwen: {
    name: 'Qwen',
    host: 'chat.qwen.ai',
    fresh: 'https://chat.qwen.ai/',
    match: 'https://chat.qwen.ai/*',
    // A Qwen chat lives at /c/<id>; the home page and settings pages are not chats.
    isChatPath: (p) => /^\/c\/[^/]+/.test(p),
    // The completion request names the chat, which is known before the page
    // moves to the chat's own address.
    chatUrl: (id) => `https://chat.qwen.ai/c/${id}`,
  },
};
const siteOf = (name) => SITES[name] || SITES.deepseek;

let connected = false;
let lastError = '';
let polling = false;
const cancelled = new Set();
const outbox = new Map();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const errText = (e) => String((e && e.message) || e);

// ---- settings ----------------------------------------------------------------

// The address is read from storage once and kept: every chunk of a reply goes
// through here, and a storage read per chunk is latency for nothing.
let cachedAddr = null;
async function settings() {
  if (cachedAddr) return { addr: cachedAddr };
  const s = await chrome.storage.local.get(['addr']);
  let addr = (s.addr || DEFAULT_ADDR).trim().replace(/\/+$/, '');
  if (!/^https?:\/\//.test(addr)) addr = 'http://' + addr;
  cachedAddr = addr;
  return { addr };
}

let lastStateKey = '';
function setState(ok, error) {
  connected = ok;
  lastError = error || '';
  chrome.action.setBadgeText({ text: ok ? 'ON' : '' });
  chrome.action.setBadgeBackgroundColor({ color: '#1a7f5a' });
  const key = ok ? 'ok' : 'bad:' + lastError;
  if (key !== lastStateKey) {
    lastStateKey = key;
  }
}

// ---- the chat Kai owns ---------------------------------------------------------
// One Kai session = one web chat (DeepSeek or Qwen). We remember which, so every message of
// the session lands in the same chat, and a new session opens a new one.

let chat = { session: null, url: null, tabId: null, site: 'deepseek' };
const chatLoaded = chrome.storage.session
  .get('chat')
  .catch(() => ({}))
  .then((s) => {
    if (s && s.chat) chat = s.chat;
  });
const persistChat = () => chrome.storage.session.set({ chat }).catch(() => {});

const pathOf = (url) => {
  try {
    return new URL(url).pathname;
  } catch (_) {
    return '';
  }
};
const isChatPath = (p) => siteOf(chat.site).isChatPath(p);

// The first time the Kai tab moves to a real chat URL, that is the chat.
function noteUrl(tabId, url) {
  if (!chat.session || chat.url || tabId !== chat.tabId) return;
  if (!isChatPath(pathOf(url))) return;
  chat.url = url;
  persistChat();
}
chrome.tabs.onUpdated.addListener((tabId, info) => {
  if (info.url) noteUrl(tabId, info.url);
});

// The completion request of a site that names its chat (Qwen: ?chat_id=...)
// tells us the chat's address right away, without waiting for the page to move.
function noteChatId(tabId, chatId, host) {
  if (!chat.session || tabId !== chat.tabId || !chatId) return;
  const site = Object.values(SITES).find((s) => s.host === host);
  if (!site || !site.chatUrl) return;
  const url = site.chatUrl(chatId);
  if (chat.url === url) return;
  chat.url = url;
  persistChat();
}

// Qwen moves the tab from "/" to "/c/<id>" a moment after a new chat's first
// reply ends. A tool result sent in that gap used to be typed into the home page
// and ended up in a new chat. Give the page a moment to arrive.
async function settleOnChat(tab, url) {
  for (let i = 0; i < 40 && pathOf(tab.url) === '/'; i++) {
    await sleep(150);
    tab = await chrome.tabs.get(tab.id);
    if (pathOf(tab.url) === pathOf(url)) break;
  }
  return tab;
}

// ---- talking to Kai ------------------------------------------------------------

async function pollLoop() {
  if (polling) return;
  polling = true;
  try {
    for (;;) {
      const { addr } = await settings();
      try {
        const r = await fetch(`${addr}/v1/poll`, { headers: HEADERS });
        if (r.status === 200) {
          setState(true);
          handleCommand(await r.json());
        } else if (r.status === 204) {
          setState(true);
        } else {
          setState(false, `Kai answered HTTP ${r.status}`);
          await sleep(3000);
        }
      } catch (_) {
        setState(false, 'Kai is not running (start it with: kai)');
        await sleep(2000);
      }
    }
  } finally {
    polling = false;
  }
}

async function post(ev) {
  const { addr } = await settings();
  try {
    const r = await fetch(`${addr}/v1/event`, {
      method: 'POST',
      headers: { ...HEADERS, 'Content-Type': 'application/json' },
      body: JSON.stringify(ev),
    });
    if (!r.ok) {
      lastError = `Kai answered HTTP ${r.status}`;
    }
    return r.ok;
  } catch (e) {
    lastError = errText(e);
    return false;
  }
}

// Chunks are sent in order; while one is in flight, newer ones are merged.
function enqueue(id, part) {
  if (cancelled.has(id)) return;
  let o = outbox.get(id);
  if (!o) {
    o = { id, buf: '', final: null, busy: false, chunks: 0, bytes: 0 };
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
        o.chunks++;
        o.bytes += data.length;
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

// ---- commands from Kai ---------------------------------------------------------

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
  post({ id: cmd.id, type: 'accepted' });
  try {
    const prep = await prepareTab(cmd);
    if (prep.lost) {
      await post({ id: cmd.id, type: 'chat_lost', message: prep.lost });
      return;
    }
    const res = await sendToTab(prep.tab.id, { type: 'send', id: cmd.id, prompt: cmd.prompt }, siteOf(cmd.site));
    if (!res || !res.ok) {
      const message = (res && res.error) || `the ${siteOf(cmd.site).name} page did not respond`;
      await post({ id: cmd.id, type: 'error', message });
    }
  } catch (e) {
    await post({ id: cmd.id, type: 'error', message: errText(e) });
  }
}

async function findTab(site) {
  const tabs = await chrome.tabs.query({ url: site.match });
  return tabs.find((t) => t.id === chat.tabId) || tabs[0] || null;
}

async function openTab(url, site) {
  const tab = await chrome.tabs.create({ url, active: false });
  await waitLoaded(tab.id, site);
  return chrome.tabs.get(tab.id);
}

async function navigate(tabId, url, site) {
  const loaded = waitLoaded(tabId, site);
  await chrome.tabs.update(tabId, { url });
  await loaded;
  return chrome.tabs.get(tabId);
}

// Decide which tab and chat a send goes to.
async function prepareTab(cmd) {
  await chatLoaded;
  const site = siteOf(cmd.site);
  let tab = await findTab(site);

  if (cmd.newChat) {
    chat = { session: cmd.session, url: null, tabId: null, site: cmd.site || 'deepseek' };
    if (!tab) tab = await openTab(site.fresh, site);
    else if (pathOf(tab.url) !== '/') tab = await navigate(tab.id, site.fresh, site);
    chat.tabId = tab.id;
    persistChat();
    return { tab };
  }

  if (!chat.session || chat.session !== cmd.session) {
    return { lost: `this extension does not know session ${cmd.session} (it has ${chat.session})` };
  }
  if ((chat.site || 'deepseek') !== (cmd.site || 'deepseek')) {
    return { lost: `this session's chat is on ${siteOf(chat.site).host}, not ${site.host}` };
  }
  if (!tab) {
    if (!chat.url) return { lost: `the ${site.name} tab was closed before the chat got an address` };
    tab = await openTab(chat.url, site);
  } else if (chat.url && pathOf(tab.url) !== pathOf(chat.url)) {
    if (site.chatUrl) tab = await settleOnChat(tab, chat.url);
  }
  if (tab && chat.url && pathOf(tab.url) !== pathOf(chat.url)) {
    tab = await navigate(tab.id, chat.url, site);
  } else if (!chat.url && isChatPath(pathOf(tab.url))) {
    chat.url = tab.url;
  }
  chat.tabId = tab.id;
  persistChat();
  return { tab };
}

function waitLoaded(tabId, site, timeout = 25000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      chrome.tabs.onUpdated.removeListener(listener);
      reject(new Error(`the ${site.name} page took too long to load`));
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

// Tabs that were open before the extension was installed or reloaded have no
// content script yet: put it there.
async function injectInto(tabId) {
  try {
    await chrome.scripting.executeScript({ target: { tabId }, files: ['inject.js'], world: 'MAIN' });
    await chrome.scripting.executeScript({ target: { tabId }, files: ['content.js'] });
    return true;
  } catch (e) {
    return false;
  }
}

async function injectAll() {
  for (const site of Object.values(SITES)) {
    const tabs = await chrome.tabs.query({ url: site.match });
    for (const t of tabs) await injectInto(t.id);
  }
}

async function sendToTab(tabId, msg, site) {
  let injected = false;
  for (let i = 0; i < 60; i++) {
    try {
      return await chrome.tabs.sendMessage(tabId, msg);
    } catch (e) {
      if (i === 3 && !injected) {
        injected = await injectInto(tabId);
      }
      await sleep(200);
    }
  }
  throw new Error(`could not reach the ${site.name} page: reload the ${site.host} tab and try again`);
}

// ---- self-test (popup button) --------------------------------------------------

async function selfTest() {
  const out = [];
  const add = (ok, msg) => {
    out.push((ok ? 'OK    ' : 'FAIL  ') + msg);
  };
  try {
    const { addr } = await settings();
    const r = await fetch(`${addr}/v1/status`, { headers: HEADERS });
    add(r.ok, `Kai answered at ${addr} (HTTP ${r.status})`);
  } catch (e) {
    add(false, 'Kai is not reachable: start it with "kai" in a terminal');
  }
  let any = false;
  for (const site of Object.values(SITES)) {
    const tab = await findTab(site);
    if (!tab) continue;
    any = true;
    await selfTestSite(site, tab, add);
  }
  if (!any) {
    const hosts = Object.values(SITES).map((s) => s.host).join(' or ');
    add(false, `no ${hosts} tab is open: open the chat Kai should use and log in`);
  }
  return out;
}

async function selfTestSite(site, tab, add) {
  add(true, `${site.name} tab found (${pathOf(tab.url) || '/'})`);
  let probe = null;
  for (let attempt = 0; attempt < 2 && !probe; attempt++) {
    try {
      probe = await chrome.tabs.sendMessage(tab.id, { type: 'probe' });
    } catch (_) {
      if (attempt === 0) await injectInto(tab.id);
      await sleep(400);
    }
  }
  if (!probe) {
    add(false, `the ${site.name} page does not answer: reload the tab`);
    return;
  }
  add(probe.hooked, probe.hooked ? `${site.name}: network hook active (v${probe.hook.version})` : `${site.name}: network hook is NOT active: reload the tab`);
  add(probe.hasInput, probe.hasInput ? `${site.name}: chat input box found` : `${site.name}: chat input box NOT found (logged in? page loaded?)`);
  if (probe.loggedOut) add(false, `${site.name}: the tab shows the login page`);
}

// ---- messages from the page and the popup ----------------------------------------

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  switch (msg.type) {
    case 'status':
      sendResponse({ connected, lastError, version: chrome.runtime.getManifest().version, chatUrl: chat.url });
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
    case 'sent':
      post({ id: msg.id, type: 'sent' });
      return;
    case 'chat_id':
      if (sender.tab) noteChatId(sender.tab.id, msg.chatId, msg.host);
      return;
    case 'page_url':
      if (sender.tab) noteUrl(sender.tab.id, msg.url);
      return;
    case 'self_test':
      selfTest().then((lines) => sendResponse({ lines }));
      return true;
  }
});

// ---- keep the worker alive -----------------------------------------------------

chrome.alarms.create('kai-wake', { periodInMinutes: 0.25 });
chrome.alarms.onAlarm.addListener(() => pollLoop());
setInterval(() => chrome.runtime.getPlatformInfo(() => {}), 30000);
chrome.runtime.onStartup.addListener(() => pollLoop());
chrome.runtime.onInstalled.addListener((d) => {
  injectAll();
  pollLoop();
});
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && changes.addr) {
    cachedAddr = null;
    pollLoop();
  }
});

pollLoop();
