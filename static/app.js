const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];
let state = {};
let feedSource = null;

async function api(path, opts = {}) {
  const res = await fetch(path, {headers: {'Content-Type': 'application/json'}, ...opts});
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

$('#loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const data = Object.fromEntries(new FormData(e.currentTarget));
  try {
    await api('/api/login', {method: 'POST', body: JSON.stringify(data)});
    $('#login').classList.add('hidden');
    $('#app').classList.remove('hidden');
    await refresh();
  } catch (err) {
    $('#loginError').textContent = 'Sign in failed';
  }
});

$('#logout').onclick = async () => {
  await api('/api/logout', {method: 'POST'});
  location.reload();
};

$$('nav [data-tab]').forEach(btn => btn.onclick = () => {
  $$('nav [data-tab]').forEach(b => b.classList.remove('active'));
  btn.classList.add('active');
  $$('.tab').forEach(t => t.classList.add('hidden'));
  $('#' + btn.dataset.tab).classList.remove('hidden');
  if (btn.dataset.tab === 'overview') requestAnimationFrame(() => drawChart(state.performance || []));
  if (btn.dataset.tab === 'logs') loadLogs();
});

$('#refresh').onclick = refresh;
$('#restart').onclick = async () => {
  if (!confirm('Restart wot-relay.service now?')) return;
  await api('/api/restart', {method: 'POST'});
  setTimeout(refresh, 1600);
};

async function refresh() {
  state = await api('/api/overview');
  renderOverview();
  renderSettings();
}

function renderOverview() {
  const ev = state.stats?.Events || {};
  const mem = state.stats?.Memory || {};
  const data = state.stats?.['Data Structures'] || {};
  const status = state.status || {};
  $('#relayTitle').textContent = state.config?.RELAY_NAME || state.config?.RELAY_URL || 'WoT Relay';
  $('#subtitle').textContent = `${status.ActiveState || 'unknown'} on ${state.relay_http}`;
  $('#metrics').innerHTML = [
    metric('Total events', ev['Total Events']),
    metric('Rejected', ev['Rejected Events']),
    metric('Archived', ev['Archived Events']),
    metric('Trust network', data['Trust Network']),
    metric('Goroutines', mem['Goroutines']),
  ].join('');
  fillDL('#service', {
    State: `${status.ActiveState || ''} ${status.SubState || ''}`,
    PID: status.MainPID,
    Tasks: status.NTasks,
    Memory: bytes(status.MemoryCurrent),
    CPU: ns(status.CPUUsageNSec),
    Started: status.ExecMainStartTimestamp,
    Unit: status.UnitFileState,
  });
  fillDL('#memory', {
    Allocated: kb(mem.Allocated),
    System: kb(mem.System),
    'Total allocations': mem['Total Allocations'],
    'GC cycles': mem['GC Cycles'],
    'DB disk used': bytes(state.disk?.used),
    'DB disk free': bytes(state.disk?.free),
  });
  fillDL('#network', {
    Relays: data.Relays,
    'Trust network': data['Trust Network'],
    'One hop': data['One Hop Network'],
    Followers: data['Follower Count Map'],
    'Network refreshes': state.stats?.Refreshes?.['Network Refreshes'],
    'Profile refreshes': state.stats?.Refreshes?.['Profile Refreshes'],
  });
  requestAnimationFrame(() => drawChart(state.performance || []));
}

function metric(label, value) {
  return `<div class="metric"><span>${label}</span><strong>${value ?? 'n/a'}</strong></div>`;
}

function fillDL(sel, obj) {
  $(sel).innerHTML = Object.entries(obj).map(([k, v]) => `<dt>${k}</dt><dd>${v || 'n/a'}</dd>`).join('');
}

function bytes(v) {
  v = Number(v || 0);
  if (!v) return 'n/a';
  const u = ['B','KB','MB','GB'];
  let i = 0;
  while (v > 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(i ? 1 : 0)} ${u[i]}`;
}
const kb = v => v ? bytes(Number(v) * 1024) : 'n/a';
const ns = v => v ? `${(Number(v) / 1e9 / 60).toFixed(1)} min` : 'n/a';

function drawChart(points) {
  const c = $('#chart'), ctx = c.getContext('2d');
  const rect = c.getBoundingClientRect();
  const cssW = Math.max(320, Math.floor(rect.width || c.parentElement.clientWidth || 720));
  const cssH = 220;
  const dpr = window.devicePixelRatio || 1;
  const w = c.width = cssW * dpr;
  const h = c.height = cssH * dpr;
  c.style.height = `${cssH}px`;
  ctx.clearRect(0, 0, w, h);
  ctx.save();
  ctx.scale(dpr, dpr);
  const pad = {left: 44, right: 14, top: 24, bottom: 34};
  const plotW = cssW - pad.left - pad.right;
  const plotH = cssH - pad.top - pad.bottom;
  const series = [
    {key: 'events', label: 'Events/min', color: '#55d6a5'},
    {key: 'rejected', label: 'Rejected/min', color: '#ef6b73'},
    {key: 'archived', label: 'Archived/min', color: '#f2bc5e'},
  ];
  const max = niceMax(Math.max(1, ...points.flatMap(p => series.map(s => Number(p[s.key] || 0)))));
  ctx.font = '12px system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';
  ctx.textBaseline = 'middle';
  ctx.strokeStyle = '#343c48';
  ctx.lineWidth = 1;
  ctx.fillStyle = '#9eacb7';
  for (let i = 0; i <= 4; i++) {
    const value = Math.round(max - (max / 4) * i);
    const y = pad.top + (plotH / 4) * i;
    ctx.beginPath(); ctx.moveTo(pad.left, y); ctx.lineTo(pad.left + plotW, y); ctx.stroke();
    ctx.fillText(String(value), 8, y);
  }
  ctx.beginPath(); ctx.moveTo(pad.left, pad.top); ctx.lineTo(pad.left, pad.top + plotH); ctx.lineTo(pad.left + plotW, pad.top + plotH); ctx.stroke();
  series.forEach((s, i) => {
    const x = pad.left + i * 112;
    ctx.fillStyle = s.color; ctx.fillRect(x, 8, 10, 10);
    ctx.fillStyle = '#eef3f6'; ctx.fillText(s.label, x + 16, 13);
  });
  if (points.length < 2) {
    ctx.fillStyle = '#9eacb7';
    ctx.textAlign = 'center';
    ctx.fillText('Waiting for journal samples', pad.left + plotW / 2, pad.top + plotH / 2);
  } else {
    series.forEach(s => drawLine(points.map(p => Number(p[s.key] || 0)), s.color));
    ctx.fillStyle = '#9eacb7';
    ctx.textAlign = 'left';
    ctx.fillText('oldest', pad.left, pad.top + plotH + 22);
    ctx.textAlign = 'right';
    ctx.fillText('latest', pad.left + plotW, pad.top + plotH + 22);
  }
  drawXAxisLabels(points.length);
  ctx.restore();
  $('#activityLabel').textContent = `${points.length} journal samples; x-axis is sample number, y-axis is per-minute count`;
  function drawLine(vals, color) {
    ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.beginPath();
    vals.forEach((v, i) => {
      const x = pad.left + (plotW / (vals.length - 1)) * i;
      const y = pad.top + plotH - (Number(v) / max) * plotH;
      i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
    });
    ctx.stroke();
  }
  function drawXAxisLabels(count) {
    const labels = count > 1 ? 5 : 1;
    ctx.fillStyle = '#9eacb7';
    ctx.textAlign = 'center';
    for (let i = 0; i < labels; i++) {
      const ratio = labels === 1 ? 0 : i / (labels - 1);
      const sample = count <= 1 ? 1 : Math.max(1, Math.round(1 + ratio * (count - 1)));
      const x = pad.left + ratio * plotW;
      ctx.fillText(String(sample), x, pad.top + plotH + 10);
    }
  }
}

function niceMax(n) {
  const pow = Math.pow(10, Math.floor(Math.log10(n)));
  const scaled = n / pow;
  const nice = scaled <= 2 ? 2 : scaled <= 5 ? 5 : 10;
  return nice * pow;
}

function renderSettings() {
  const cfg = state.config || {};
  $$('#settingsForm [name]').forEach(el => el.value = cfg[el.name] || '');
}

$('#settingsForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  await saveSettings(false);
});
$('#saveRestart').onclick = async () => saveSettings(true);

async function saveSettings(restart) {
  const data = Object.fromEntries(new FormData($('#settingsForm')));
  await api('/api/config', {method: 'POST', body: JSON.stringify(data)});
  if (restart) await api('/api/restart', {method: 'POST'});
  await refresh();
}

$('#loadNotes').onclick = loadNotes;
$('#startFeed').onclick = startFeed;
$('#stopFeed').onclick = stopFeed;
$('#clearFeed').onclick = () => $('#liveFeed').innerHTML = '';

function startFeed() {
  stopFeed();
  $('#liveFeed').innerHTML ||= '<p class="emptyState">Waiting for new relay events...</p>';
  const kind = $('#feedKind').value || '1';
  feedSource = new EventSource('/api/feed?kind=' + encodeURIComponent(kind));
  $('#feedStatus').textContent = `Connecting for kind ${kind} events`;
  feedSource.addEventListener('status', (event) => {
    const msg = JSON.parse(event.data);
    $('#feedStatus').textContent = `${msg.status} for kind ${kind} events`;
  });
  feedSource.addEventListener('note', (event) => {
    const ev = JSON.parse(event.data);
    prependLiveNote(ev);
  });
  feedSource.addEventListener('error', () => {
    $('#feedStatus').textContent = 'Connection interrupted; retrying';
  });
}

function stopFeed() {
  if (feedSource) feedSource.close();
  feedSource = null;
  $('#feedStatus').textContent = 'Stopped';
}

function prependLiveNote(ev) {
  const feed = $('#liveFeed');
  if (feed.querySelector('.emptyState')) feed.innerHTML = '';
  feed.insertAdjacentHTML('afterbegin', noteCard(ev));
  while (feed.children.length > 40) feed.lastElementChild.remove();
}

async function loadNotes() {
  const qs = new URLSearchParams({
    search: $('#noteSearch').value,
    author: $('#noteAuthor').value,
    kind: $('#noteKind').value,
    limit: '50',
  });
  $('#notesList').innerHTML = '<p class="hint">Loading...</p>';
  const data = await api('/api/notes?' + qs);
  $('#notesList').innerHTML = (data.events || []).map(noteCard).join('') || '<p class="hint">No notes returned.</p>';
}

function noteCard(ev) {
  const date = ev.created_at ? new Date(ev.created_at * 1000).toISOString() : '';
  const content = escapeHTML(ev.content || JSON.stringify(ev, null, 2));
  return `<article class="note"><header><span>kind ${ev.kind}</span><span>${date}</span><span>${ev.pubkey || ''}</span></header><pre>${content}</pre></article>`;
}

async function loadLogs() {
  const data = await api('/api/logs');
  $('#logBox').textContent = data.logs || '';
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;', "'":'&#39;'}[ch]));
}

refresh().then(() => {
  $('#login').classList.add('hidden');
  $('#app').classList.remove('hidden');
}).catch(() => {});
