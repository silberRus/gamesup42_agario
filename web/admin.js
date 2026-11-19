'use strict';

const $ = (id) => document.getElementById(id);
const BASE = location.pathname.replace(/\/admin\/?$/, ''); // URL prefix the app is served under

const WINDOW_MS = 5 * 60 * 1000;         // time span of the charts
const SERIES_COLORS = ['#3987e5', '#d95926'];

// One card per chart; keys are fields of the server's Sample (stats.go).
const CHARTS = [
  { title: 'Время тика, мс', keys: ['tickAvg', 'tickP99'], labels: ['среднее', 'p99'] },
  { title: 'CPU процесса, %', keys: ['cpu'] },
  { title: 'RAM, МБ', keys: ['ram'] },
  { title: 'Горутины', keys: ['goroutines'] },
  { title: 'Соединения', keys: ['conns'] },
  { title: 'Сообщений в секунду', keys: ['msgsIn', 'msgsOut'], labels: ['входящие', 'исходящие'] },
  { title: 'Исходящий трафик, КБ/с', keys: ['kbOut'] },
  { title: 'Сущности в мире', keys: ['entities'] },
];

let ws = null;
let samples = [];     // one per second, oldest first
let pressing = false; // a mouse button is down on the player table

const esc = (text) => String(text).replace(/[&<>"']/g, (ch) => `&#${ch.charCodeAt(0)};`);
const pad = (n) => String(n).padStart(2, '0');
const num = (v) => v == null ? 'n/a' : Number.isInteger(v) ? String(v) : v.toFixed(v < 10 ? 2 : 1);
const clock = (ms) => new Date(ms).toLocaleTimeString('ru-RU');
const dateTime = (ms) => new Date(ms).toLocaleString('ru-RU');

function duration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  return (h ? h + ':' : '') + pad(Math.floor(s / 60) % 60) + ':' + pad(s % 60);
}

// Humans are either the owner or, presumably, someone reviewing the assignment.
function tag(p) {
  if (p.bot) return '';
  return p.owner ? '<span class="tag me">👑 это я</span>' : '<span class="tag">🕵️ Похоже, это проверяющий</span>';
}

// ---- live data ----

function connect() {
  ws = new WebSocket(location.origin.replace(/^http/, 'ws') + BASE + '/admin/ws');
  ws.onopen = () => setStatus(true);
  ws.onmessage = (e) => render(JSON.parse(e.data));
  ws.onclose = () => { setStatus(false); setTimeout(connect, 2000); };
}

function setStatus(on) {
  $('status').className = on ? 'on' : 'off';
  $('status').textContent = on ? 'на связи' : 'нет связи';
}

function send(cmd) {
  if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(cmd));
}

function render(m) {
  if (m.chart) samples = m.chart; // the first message carries the history
  if (!samples.length || m.stats.t > samples[samples.length - 1].t) samples.push(m.stats);
  while (samples[0].t < m.stats.t - WINDOW_MS) samples.shift();
  CHARTS.forEach(drawChart);

  if (document.activeElement !== $('bots')) $('bots').value = m.bots;
  if (document.activeElement !== $('difficulty')) $('difficulty').value = m.difficulty;
  if (m.isOwner) showOwner();

  if (!pressing) { // replacing a row under a pressed button would swallow the click
    $('players').innerHTML = m.players.map((p) => `<tr>
      <td>${esc(p.name)}${tag(p)}</td>
      <td>${p.bot ? 'бот' : 'человек'}</td>
      <td>${p.bot ? '—' : esc(p.ip)}</td>
      <td class="ua" title="${esc(p.ua)}">${p.bot ? '—' : esc(p.ua)}</td>
      <td>${clock(p.joined)}</td>
      <td class="num">${duration(m.now - p.joined)}</td>
      <td class="num">${p.bot ? '—' : p.ping + ' мс'}</td>
      <td class="num">${p.mass || 'съеден'}</td>
      <td><button data-kick="${p.id}">Выгнать</button></td>
    </tr>`).join('') || '<tr><td class="empty" colspan="9">В мире пусто</td></tr>';
  }

  $('sessions').innerHTML = m.sessions.map((s) => `<tr>
    <td>${esc(s.name)}${tag(s)}</td>
    <td>${esc(s.ip)}</td>
    <td class="ua" title="${esc(s.ua)}">${esc(s.ua)}</td>
    <td>${dateTime(s.start)}</td>
    <td>${dateTime(s.end)}</td>
    <td class="num">${s.maxMass}</td>
  </tr>`).join('') || '<tr><td class="empty" colspan="6">Пока никто не заходил</td></tr>';
}

// ---- charts ----

for (const c of CHARTS) {
  const card = document.createElement('div');
  card.className = 'chart';
  card.innerHTML = `<h3>${c.title}</h3><div class="value"></div><canvas></canvas>`;
  c.value = card.querySelector('.value');
  c.canvas = card.querySelector('canvas');
  c.hover = null; // pointer position over the chart, 0..1
  c.canvas.addEventListener('pointermove', (e) => { c.hover = e.offsetX / c.canvas.clientWidth; drawChart(c); });
  c.canvas.addEventListener('pointerleave', () => { c.hover = null; drawChart(c); });
  $('charts').append(card);
}

function drawChart(c) {
  if (!samples.length) return;
  const dpr = window.devicePixelRatio || 1;
  const w = c.canvas.clientWidth, h = c.canvas.clientHeight;
  c.canvas.width = w * dpr; // also clears the canvas
  c.canvas.height = h * dpr;
  const g = c.canvas.getContext('2d');
  g.scale(dpr, dpr);

  const end = samples[samples.length - 1].t;
  const peak = Math.max(...samples.flatMap((s) => c.keys.map((key) => s[key] || 0)));
  const xOf = (t) => (1 - (end - t) / WINDOW_MS) * w;
  const yOf = (v) => h - 1 - v / (peak * 1.1 || 1) * (h - 14);

  // The sample to show in numbers: the latest, or the one under the pointer.
  let shown = samples[samples.length - 1];
  if (c.hover != null) {
    const t = end - (1 - c.hover) * WINDOW_MS;
    shown = samples.reduce((best, s) => Math.abs(s.t - t) < Math.abs(best.t - t) ? s : best);
    g.strokeStyle = 'rgba(255, 255, 255, .3)';
    g.beginPath();
    g.moveTo(xOf(shown.t), 0);
    g.lineTo(xOf(shown.t), h);
    g.stroke();
  }

  g.strokeStyle = 'rgba(255, 255, 255, .14)'; // zero line
  g.beginPath();
  g.moveTo(0, h - 0.5);
  g.lineTo(w, h - 0.5);
  g.stroke();
  g.fillStyle = '#8f95b8';                    // scale: the peak of the window
  g.font = '10px system-ui, sans-serif';
  g.fillText('макс. ' + num(peak), 0, 9);

  c.keys.forEach((key, i) => {
    g.beginPath();
    for (const s of samples) if (s[key] != null) g.lineTo(xOf(s.t), yOf(s[key]));
    g.strokeStyle = g.fillStyle = SERIES_COLORS[i];
    g.lineWidth = 2;
    g.lineJoin = 'round';
    g.stroke();
    if (c.hover != null && shown[key] != null) {
      g.beginPath();
      g.arc(xOf(shown.t), yOf(shown[key]), 3.5, 0, Math.PI * 2);
      g.fill();
    }
  });

  c.value.innerHTML = c.keys.map((key, i) => c.labels
    ? `<span><i style="background:${SERIES_COLORS[i]}"></i>${c.labels[i]} <b>${num(shown[key])}</b></span>`
    : `<b>${num(shown[key])}</b>`
  ).join('') + (c.hover != null ? `<span>${clock(shown.t)}</span>` : '');
}

// ---- controls ----

$('botsForm').addEventListener('submit', (e) => {
  e.preventDefault();
  send({ cmd: 'bots', n: Number($('bots').value) });
  $('bots').blur();
});

$('difficulty').addEventListener('change', () => {
  send({ cmd: 'difficulty', level: $('difficulty').value });
  $('difficulty').blur();
});

$('reset').addEventListener('click', () => {
  if (confirm('Сбросить мир? Все начнут с нуля.')) send({ cmd: 'reset' });
});

$('players').addEventListener('pointerdown', () => { pressing = true; });
window.addEventListener('pointerup', () => setTimeout(() => { pressing = false; }));
$('players').addEventListener('click', (e) => {
  const id = e.target.dataset.kick;
  if (id) send({ cmd: 'kick', id: Number(id) });
});

function showOwner() {
  $('markOwner').textContent = '👑 Этот браузер отмечен как мой';
  $('markOwner').disabled = true;
}

$('markOwner').addEventListener('click', async () => {
  // location.origin: a relative URL fails if the page was opened as user:pass@host
  const res = await fetch(location.origin + BASE + '/admin/owner', { method: 'POST' });
  if (!res.ok) return;
  showOwner();
  $('ownerNote').textContent = 'Готово. Сессии, начатые в этом браузере с этого момента, будут с пометкой «это я» — обновите вкладку с игрой.';
});

window.addEventListener('resize', () => CHARTS.forEach(drawChart));
connect();
