const $ = (id) => document.getElementById(id);

chrome.storage.local.get(['addr']).then((s) => {
  $('addr').value = s.addr || '';
});

$('save').addEventListener('click', async () => {
  await chrome.storage.local.set({ addr: $('addr').value.trim() });
  chrome.runtime.sendMessage({ type: 'poll_now' });
});

$('selftest').addEventListener('click', async () => {
  $('test').textContent = 'Running…';
  const r = await chrome.runtime.sendMessage({ type: 'self_test' });
  $('test').textContent = r.lines.join('\n');
});

async function logs() {
  return (await chrome.runtime.sendMessage({ type: 'get_logs' })).text;
}

$('download').addEventListener('click', async () => {
  const blob = new Blob([await logs()], { type: 'text/plain' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'logs.txt';
  a.click();
});

$('copy').addEventListener('click', async () => {
  await navigator.clipboard.writeText(await logs());
  $('test').textContent = 'Logs copied.';
});

$('clear').addEventListener('click', async () => {
  await chrome.runtime.sendMessage({ type: 'clear_logs' });
  $('test').textContent = 'Logs cleared.';
});

async function refresh() {
  const el = $('state');
  try {
    const st = await chrome.runtime.sendMessage({ type: 'status' });
    if (st.connected) {
      el.textContent = 'Connected to Kai';
      el.className = 'ok';
    } else {
      el.textContent = 'Not connected' + (st.lastError ? `: ${st.lastError}` : '');
      el.className = 'bad';
    }
  } catch (_) {
    el.textContent = 'Starting…';
    el.className = 'bad';
  }
}

refresh();
setInterval(refresh, 1000);
