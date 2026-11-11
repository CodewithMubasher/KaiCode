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

// ---- logging -----------------------------------------------------------------
// Kept in memory, saved to chrome.storage (so it survives the worker being
// suspended), downloadable from the popup, and shipped to Kai, which writes it
// into logs.txt next to its own lines.

const MAX_LOG = 1000;
let logBuf = [];
let pending = [];
let dirty = false;

const logsReady = chrome.storage.local.get('logs').then((s) => {
  if (Array.isArray(s.logs)) logBuf = s.logs.concat(logBuf).slice(-MAX_LOG);
});

function log(level, msg, src = 'bg') {
  const line = { t: Date.now(), src, lvl: level, msg: String(msg) };
  logBuf.push(line);
  if (logBuf.length > MAX_LOG) logBuf.shift();
  pending.push(line);
  if (pending.length > 300) pending.shift();
  dirty = true;
}

const fmt = (l) => `${new Date(l.t).toISOString()} [${l.src}] ${l.lvl.toUpperCase()} ${l.msg}`;

async function saveAndShip() {
  if (dirty) {
    dirty = false;
    await logsReady;
    chrome.storage.local.set({ logs: logBuf }).catch(() => {});
  }
  if (connected && pending.length) {
    const batch = pending.splice(0, pending.length);
    try {
      const { addr } = await settings();
      const r = await fetch(`${addr}/v1/log`, {
        method: 'POST',
        headers: { ...HEADERS, 'Content-Type': 'application/json' },
        body: JSON.stringify({ lines: batch }),
      });
      if (!r.ok) pending.unshift(...batch);
    } catch (_) {
      pending.unshift(...batch);
    }
  }
}
setInterval(saveAndShip, 1500);

// ---- settings ----------------------------------------------------------------

async function settings() {
  const s = await chrome.storage.local.get(['addr']);
  let addr = (s.addr || DEFAULT_ADDR).trim().replace(/\/+$/, '');
  if (!/^https?:\/\//.test(addr)) addr = 'http://' + addr;
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
    log(ok ? 'info' : 'warn', ok ? 'connected to Kai' : `not connected to Kai: ${lastError}`);
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
  log('info', `this session's chat is ${url}`);
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
  log('info', `this session's chat is ${url} (named by the completion request)`);
}

// Qwen moves the tab from "/" to "/c/<id>" a moment after a new chat's first
// reply ends. A tool result sent in that gap used to be typed into the home page
// and ended up in a new chat. Give the page a moment to arrive.
async function settleOnChat(tab, url) {
  for (let i = 0; i < 12 && pathOf(tab.url) === '/'; i++) {
    await sleep(500);
    tab = await chrome.tabs.get(tab.id);
    if (pathOf(tab.url) === pathOf(url)) log('info', `the tab arrived at ${pathOf(url)}`);
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
      log('warn', `event ${ev.type} was refused: HTTP ${r.status}`);
    }
    return r.ok;
  } catch (e) {
    lastError = errText(e);
    log('warn', `could not send event ${ev.type} to Kai: ${lastError}`);
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
        if (o.chunks === 1) log('info', `${o.id}: first reply data (${data.length} chars) goes to Kai`);
        await post({ id: o.id, type: 'chunk', data });
      } else {
        const final = o.final;
        o.final = null;
        log('info', `${o.id}: ${final.type} after ${o.chunks} posts, ${o.bytes} chars${final.message ? ': ' + final.message : ''}`);
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
    log('info', `${cmd.id}: cancelled by Kai`);
    cancelled.add(cmd.id);
    outbox.delete(cmd.id);
    if (cancelled.size > 50) cancelled.delete(cancelled.values().next().value);
  } else if (cmd.type === 'send') {
    runSend(cmd);
  }
}

async function runSend(cmd) {
  log('info', `${cmd.id}: send received (${siteOf(cmd.site).host}, session ${cmd.session}, newChat=${cmd.newChat}, ${(cmd.prompt || '').length} chars)`);
  post({ id: cmd.id, type: 'accepted' });
  try {
    const prep = await prepareTab(cmd);
    if (prep.lost) {
      log('warn', `${cmd.id}: chat lost: ${prep.lost}`);
      await post({ id: cmd.id, type: 'chat_lost', message: prep.lost });
      return;
    }
    let res;
    try {
      res = await sendToTab(prep.tab.id, { type: 'send', id: cmd.id, prompt: cmd.prompt }, siteOf(cmd.site));
    } finally {
      await detachDebugger(prep.tab.id);
    }
    if (!res || !res.ok) {
      const message = (res && res.error) || `the ${siteOf(cmd.site).name} page did not respond`;
      log('error', `${cmd.id}: could not send: ${message}`);
      await post({ id: cmd.id, type: 'error', message });
    } else {
      log('info', `${cmd.id}: the page sent the message`);
    }
  } catch (e) {
    log('error', `${cmd.id}: ${errText(e)}`);
    await post({ id: cmd.id, type: 'error', message: errText(e) });
  }
}

async function findTab(site) {
  const tabs = await chrome.tabs.query({ url: site.match });
  return tabs.find((t) => t.id === chat.tabId) || tabs[0] || null;
}

async function openTab(url, site) {
  log('info', `opening ${url} in a new tab`);
  const tab = await chrome.tabs.create({ url, active: false });
  await waitLoaded(tab.id, site);
  return chrome.tabs.get(tab.id);
}

async function navigate(tabId, url, site) {
  log('info', `tab ${tabId}: going to ${url}`);
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
    log('info', `the tab left Kai's chat (${pathOf(tab.url)}): going back`);
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
    log('info', `tab ${tabId}: scripts injected`);
    return true;
  } catch (e) {
    log('error', `tab ${tabId}: could not inject scripts: ${errText(e)}`);
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
  for (let i = 0; i < 24; i++) {
    try {
      return await chrome.tabs.sendMessage(tabId, msg);
    } catch (e) {
      if (i === 2 && !injected) {
        log('warn', `tab ${tabId} does not answer (${errText(e)}): injecting scripts`);
        injected = await injectInto(tabId);
      }
      await sleep(500);
    }
  }
  throw new Error(`could not reach the ${site.name} page: reload the ${site.host} tab and try again`);
}

// ---- trusted input ------------------------------------------------------------
// content.js uses dispatchEvent()/el.click(), which produce isTrusted === false.
// The debugger protocol drives Chrome's own input pipeline instead, so the page
// receives the same kind of events a real user produces. It needs the
// "debugger" permission and cannot attach while DevTools is open on that tab;
// content.js then falls back to the DOM path.

const TRUSTED_IDLE = 5000;
const debugging = new Map(); // tabId -> { timer }

async function debugSend(tabId, method, params) {
  if (!chrome.debugger) throw new Error('the debugger permission is not active: reload the extension');
  let d = debugging.get(tabId);
  if (!d) {
    await chrome.debugger.attach({ tabId }, '1.3');
    d = { timer: null };
    debugging.set(tabId, d);
    log('info', `tab ${tabId}: trusted input attached`);
  }
  if (d.timer) {
    clearTimeout(d.timer);
    d.timer = null;
  }
  try {
    return await chrome.debugger.sendCommand({ tabId }, method, params);
  } finally {
    d.timer = setTimeout(() => detachDebugger(tabId), TRUSTED_IDLE);
  }
}

async function detachDebugger(tabId) {
  const d = debugging.get(tabId);
  if (!d) return;
  if (d.timer) clearTimeout(d.timer);
  debugging.delete(tabId);
  try {
    await chrome.debugger.detach({ tabId });
  } catch (_) {
    /* already gone (tab closed, worker suspended) */
  }
}

async function trustedInput(msg, sender) {
  const tabId = sender.tab && sender.tab.id;
  if (tabId == null) return { ok: false, error: 'no tab' };
  try {
    switch (msg.op) {
      case 'insertText':
        if (typeof msg.text !== 'string') return { ok: false, error: 'no text' };
        await debugSend(tabId, 'Input.insertText', { text: msg.text });
        return { ok: true };
      case 'key':
        if (msg.key !== 'Enter') return { ok: false, error: `unsupported key ${msg.key}` };
        await debugSend(tabId, 'Input.dispatchKeyEvent', {
          type: 'keyDown',
          key: 'Enter',
          code: 'Enter',
          windowsVirtualKeyCode: 13,
          nativeVirtualKeyCode: 13,
          text: '\r',
          unmodifiedText: '\r',
        });
        await debugSend(tabId, 'Input.dispatchKeyEvent', {
          type: 'keyUp',
          key: 'Enter',
          code: 'Enter',
          windowsVirtualKeyCode: 13,
          nativeVirtualKeyCode: 13,
        });
        return { ok: true };
      case 'click': {
        const x = Number(msg.x);
        const y = Number(msg.y);
        if (!Number.isFinite(x) || !Number.isFinite(y)) return { ok: false, error: 'bad click coordinates' };
        await debugSend(tabId, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
        await debugSend(tabId, 'Input.dispatchMouseEvent', {
          type: 'mousePressed',
          x,
          y,
          button: 'left',
          buttons: 1,
          clickCount: 1,
        });
        await debugSend(tabId, 'Input.dispatchMouseEvent', {
          type: 'mouseReleased',
          x,
          y,
          button: 'left',
          buttons: 0,
          clickCount: 1,
        });
        return { ok: true };
      }
      default:
        return { ok: false, error: `unknown operation ${msg.op}` };
    }
  } catch (e) {
    return { ok: false, error: errText(e) };
  }
}

if (chrome.debugger) {
  chrome.debugger.onDetach.addListener((source, reason) => {
    if (source.tabId == null) return;
    const d = debugging.get(source.tabId);
    if (d && d.timer) clearTimeout(d.timer);
    debugging.delete(source.tabId);
    log('info', `tab ${source.tabId}: trusted input detached (${reason})`);
  });
}

chrome.tabs.onRemoved.addListener((tabId) => {
  const d = debugging.get(tabId);
  if (d && d.timer) clearTimeout(d.timer);
  debugging.delete(tabId);
});

// ---- self-test (popup button) --------------------------------------------------

async function selfTest() {
  const out = [];
  const add = (ok, msg) => {
    out.push((ok ? 'OK    ' : 'FAIL  ') + msg);
    log(ok ? 'info' : 'warn', `self-test: ${msg}`);
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
  if (chrome.debugger) {
    try {
      await chrome.debugger.attach({ tabId: tab.id }, '1.3');
      await chrome.debugger.detach({ tabId: tab.id });
      add(true, `${site.name}: trusted input is available (debugger protocol)`);
    } catch (e) {
      add(false, `${site.name}: trusted input unavailable (${errText(e)}): Kai will use DOM events`);
    }
  } else {
    add(false, 'trusted input unavailable: the debugger permission is missing');
  }
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
    case 'trusted':
      trustedInput(msg, sender).then(sendResponse);
      return true;
    case 'sent':
      log('info', `${msg.id}: the page started the completion request`);
      post({ id: msg.id, type: 'sent' });
      return;
    case 'chat_id':
      if (sender.tab) noteChatId(sender.tab.id, msg.chatId, msg.host);
      return;
    case 'page_url':
      if (sender.tab) noteUrl(sender.tab.id, msg.url);
      return;
    case 'log':
      log(msg.level || 'info', msg.msg, msg.src || 'page');
      return;
    case 'get_logs':
      logsReady.then(() => sendResponse({ text: logBuf.map(fmt).join('\n') + '\n' }));
      return true;
    case 'clear_logs':
      logBuf = [];
      pending = [];
      chrome.storage.local.remove('logs');
      sendResponse({});
      return;
    case 'self_test':
      selfTest().then((lines) => sendResponse({ lines }));
      return true;
  }
});

// ---- keep the worker alive -----------------------------------------------------

chrome.alarms.create('kai-wake', { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener(() => pollLoop());
setInterval(() => chrome.runtime.getPlatformInfo(() => {}), 20000);
chrome.runtime.onStartup.addListener(() => pollLoop());
chrome.runtime.onInstalled.addListener((d) => {
  log('info', `extension ${d.reason} (v${chrome.runtime.getManifest().version})`);
  injectAll();
  pollLoop();
});
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && changes.addr) pollLoop();
});

log('info', `worker started (v${chrome.runtime.getManifest().version})`);
pollLoop();
