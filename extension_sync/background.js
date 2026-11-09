// Pushes this browser's DeepSeek session to Kai.
const SERVER = "http://127.0.0.1:8765/_sync";
const CHAT = "https://chat.deepseek.com/";

let hdr = {};
let lastBody = "", lastSent = 0, timer = null;

async function loadHdr() {
  if (!hdr.token) hdr = (await chrome.storage.local.get("hdr")).hdr || {};
  return hdr;
}

chrome.webRequest.onBeforeSendHeaders.addListener(
  (d) => {
    const h = {};
    for (const x of d.requestHeaders || []) h[x.name.toLowerCase()] = x.value;
    if (!h.authorization) return;
    const upd = {
      token: h.authorization.replace(/^Bearer\s+/i, ""),
      device_id: h["x-device-id"],
      user_agent: h["user-agent"],
      client_version: h["x-client-version"],
      locale: h["x-client-locale"],
      timezone_offset: h["x-client-timezone-offset"],
    };
    loadHdr().then(() => {
      for (const [k, v] of Object.entries(upd)) if (v) hdr[k] = v;
      hdr.user_agent = hdr.user_agent || navigator.userAgent;
      chrome.storage.local.set({ hdr });
      schedulePush();
    });
  },
  { urls: ["https://chat.deepseek.com/api/*"] },
  ["requestHeaders", "extraHeaders"]
);

chrome.cookies.onChanged.addListener((c) => {
  if (c.cookie.domain.endsWith("deepseek.com")) schedulePush();
});

function schedulePush() {
  clearTimeout(timer);
  timer = setTimeout(pushNow, 1500);
}

async function pushNow() {
  await loadHdr();
  if (!hdr.token || !hdr.device_id) return;
  const cookies = await chrome.cookies.getAll({ url: CHAT });
  const body = JSON.stringify({
    ...hdr,
    user_agent: hdr.user_agent || navigator.userAgent,
    cookie: cookies.map((c) => `${c.name}=${c.value}`).join("; "),
  });
  if (body === lastBody && Date.now() - lastSent < 5 * 60e3) return;
  try {
    const r = await fetch(SERVER, { method: "POST", headers: { "content-type": "application/json", "x-ds-sync": "1" }, body });
    if (r.ok) { lastBody = body; lastSent = Date.now(); }
  } catch (e) { }
}

async function ensureTab() {
  const tabs = await chrome.tabs.query({ url: CHAT + "*" });
  if (!tabs.length) {
    const t = await chrome.tabs.create({ url: CHAT, active: false, pinned: true });
    chrome.tabs.update(t.id, { autoDiscardable: false });
    return;
  }
  for (const t of tabs) {
    if (t.discarded) chrome.tabs.reload(t.id);
    else if (t.autoDiscardable) chrome.tabs.update(t.id, { autoDiscardable: false });
  }
}

chrome.alarms.create("tick", { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener(async () => { await ensureTab(); await pushNow(); });
chrome.runtime.onInstalled.addListener(() => { ensureTab(); });
chrome.runtime.onStartup.addListener(() => { ensureTab(); });