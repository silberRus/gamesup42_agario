'use strict';

// Mirrors of the server constants (game.go).
const WORLD = 4000, VIEW_W = 1920, VIEW_H = 1080;
const zoomFor = (r) => Math.max(0.35, Math.pow(45 / r, 0.4));

const INTERP_MS = 100; // draw this far in the past, so there are two snapshots to blend
const GRID = 50;

const $ = (id) => document.getElementById(id);
const canvas = $('game');
const ctx = canvas.getContext('2d');

let ws = null;
let nick = '';
let myId = 0;         // id of my current cell; changes with every life
let alive = false;
let mass = 0;
let killer = '';
let snaps = [];       // recent snapshots, oldest first
let clockOffset = 0;  // smoothed (local time - server time)
let leadersHtml = '';
let toastShown = false;
let width = 0, height = 0, dpr = 1;
let scale = 1;        // screen px per world unit
const cam = { x: WORLD / 2, y: WORLD / 2, zoom: zoomFor(18), snap: true };
const pointer = { x: 0, y: 0 }; // offset from the screen centre, px

// ---- network ----

function connect() {
  myId = 0;
  ws = new WebSocket(location.origin.replace(/^http/, 'ws') + location.pathname.replace(/\/$/, '') + '/ws');
  ws.onopen = () => send({ t: 'join', name: nick });
  ws.onmessage = (e) => onSnapshot(JSON.parse(e.data));
  ws.onclose = (e) => {
    ws = null;
    alive = false;
    snaps = [];
    $('hud').hidden = true;
    $('death').hidden = true;
    $('notice').textContent = e.code === 4000
      ? 'Вас попросили покинуть собеседование.'
      : 'Связь с сервером потеряна. Попробуйте ещё раз.';
    $('notice').hidden = false;
    $('login').hidden = false;
  };
}

function send(msg) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

function onSnapshot(s) {
  const now = performance.now();
  if (s.id !== myId) { // a new life: first join, respawn or world reset
    myId = s.id;
    alive = true;
    mass = s.m;
    killer = '';
    snaps = [];
    cam.snap = true;
    $('login').hidden = true;
    $('death').hidden = true;
    $('hud').hidden = false;
  }

  clockOffset = snaps.length ? clockOffset + (now - s.ts - clockOffset) * 0.05 : now - s.ts;
  s.byId = new Map(s.p.map((p) => [p.i, p]));
  snaps.push(s);
  if (snaps.length > 10) snaps.shift();

  for (const k of s.k || []) {
    const mine = k.vi === myId;
    if (mine) killer = k.k;
    addFeed(mine ? `Вас съел ${k.k}` : `${k.k} съел ${k.v}`, mine);
  }

  if (alive && s.m === 0) {
    alive = false;
    $('killer').textContent = killer ? `Вас съел ${killer}` : '';
    $('final').textContent = s.e;
    $('death').hidden = false;
  }
  if (alive) {
    if (!toastShown && mass < 42 && s.m >= 42) {
      toastShown = true;
      $('toast').hidden = false;
      setTimeout(() => { $('toast').hidden = true; }, 6000);
    }
    mass = s.m;
    $('mass').textContent = Math.round(s.m);
  }
  $('eaten').textContent = s.e;

  const html = s.lb.map((e, i) =>
    `<li class="${e.i === myId ? 'me' : ''}"><span>${esc(e.n)}</span>${i === 0 ? '<em>🏆 оффер</em>' : ''}<b>${e.m}</b></li>`
  ).join('');
  if (html !== leadersHtml) $('leaders').innerHTML = leadersHtml = html;
}

function esc(text) {
  return text.replace(/[&<>"']/g, (ch) => `&#${ch.charCodeAt(0)};`);
}

function addFeed(text, mine) {
  const feed = $('feed');
  const line = document.createElement('div');
  line.textContent = text;
  if (mine) line.className = 'mine';
  feed.append(line);
  while (feed.children.length > 5) feed.firstChild.remove();
  setTimeout(() => line.remove(), 6000);
}

// ---- rendering ----

function resize() {
  dpr = Math.min(window.devicePixelRatio || 1, 2);
  width = window.innerWidth;
  height = window.innerHeight;
  canvas.width = width * dpr;
  canvas.height = height * dpr;
}

// interpolate returns the players as they were INTERP_MS ago, blended between
// the two snapshots around that moment, plus the food of the earlier one.
function interpolate(now) {
  if (!snaps.length) return { players: [], food: [] };
  const t = now - clockOffset - INTERP_MS;
  let i = snaps.length - 1;
  while (i > 0 && snaps[i].ts > t) i--;
  const a = snaps[i], b = snaps[i + 1] || a;
  const k = b === a ? 0 : Math.min(Math.max((t - a.ts) / (b.ts - a.ts), 0), 1);
  const players = a.p.map((p) => {
    const q = b.byId.get(p.i) || p;
    return { ...p, x: p.x + (q.x - p.x) * k, y: p.y + (q.y - p.y) * k, r: p.r + (q.r - p.r) * k };
  });
  return { players, food: a.f };
}

function frame(now) {
  requestAnimationFrame(frame);
  const { players, food } = interpolate(now);

  const me = players.find((p) => p.i === myId);
  if (me) { // the camera follows my cell and zooms out as it grows
    cam.x = me.x;
    cam.y = me.y;
    const zoom = zoomFor(me.r);
    cam.zoom = cam.snap ? zoom : cam.zoom + (zoom - cam.zoom) * 0.08;
    cam.snap = false;
  }
  scale = cam.zoom * Math.max(width / VIEW_W, height / VIEW_H);

  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.fillStyle = '#07070f';
  ctx.fillRect(0, 0, width, height);
  // From here on draw in world coordinates.
  ctx.setTransform(dpr * scale, 0, 0, dpr * scale, dpr * (width / 2 - cam.x * scale), dpr * (height / 2 - cam.y * scale));

  drawArena();
  for (const [x, y, hue] of food) drawToken(x, y, hue);
  players.sort((p, q) => p.r - q.r); // bigger cells cover smaller ones
  for (const p of players) drawCell(p);
}

function drawArena() {
  const x0 = Math.max(0, cam.x - width / 2 / scale), x1 = Math.min(WORLD, cam.x + width / 2 / scale);
  const y0 = Math.max(0, cam.y - height / 2 / scale), y1 = Math.min(WORLD, cam.y + height / 2 / scale);
  ctx.beginPath();
  for (let x = Math.ceil(x0 / GRID) * GRID; x <= x1; x += GRID) { ctx.moveTo(x, y0); ctx.lineTo(x, y1); }
  for (let y = Math.ceil(y0 / GRID) * GRID; y <= y1; y += GRID) { ctx.moveTo(x0, y); ctx.lineTo(x1, y); }
  ctx.strokeStyle = 'rgba(90, 120, 255, 0.14)';
  ctx.lineWidth = 1 / scale;
  ctx.stroke();

  ctx.strokeStyle = ctx.shadowColor = '#ff3fd2';
  ctx.shadowBlur = 24 * dpr;
  ctx.lineWidth = 4;
  ctx.strokeRect(0, 0, WORLD, WORLD);
  ctx.shadowBlur = 0;
}

function drawToken(x, y, hue) {
  ctx.fillStyle = `hsl(${hue}, 100%, 65%)`;
  ctx.beginPath();
  ctx.moveTo(x, y - 6);
  ctx.lineTo(x + 6, y);
  ctx.lineTo(x, y + 6);
  ctx.lineTo(x - 6, y);
  ctx.fill();
}

function drawCell(p) {
  const rim = Math.max(3, p.r * 0.09);
  ctx.beginPath();
  ctx.arc(p.x, p.y, p.r - rim / 2, 0, Math.PI * 2);
  ctx.fillStyle = `hsl(${p.h}, 80%, 11%)`;
  ctx.fill();
  ctx.strokeStyle = ctx.shadowColor = `hsl(${p.h}, 100%, 60%)`;
  ctx.shadowBlur = 22 * dpr;
  ctx.lineWidth = rim;
  ctx.stroke();
  ctx.shadowBlur = 0;

  ctx.fillStyle = '#fff';
  ctx.font = `600 ${Math.max(11, p.r * 0.36)}px system-ui, sans-serif`;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText(p.n, p.x, p.y);
}

// ---- input ----

function onPointer(e) { // mouse and touch alike
  pointer.x = e.clientX - width / 2;
  pointer.y = e.clientY - height / 2;
}
canvas.addEventListener('pointermove', onPointer);
canvas.addEventListener('pointerdown', onPointer);

// The cell heads for the point under the cursor; tell the server 20 times a second.
setInterval(() => {
  if (alive) send({ t: 'm', x: Math.round(pointer.x / scale), y: Math.round(pointer.y / scale) });
}, 50);

$('join').addEventListener('submit', (e) => {
  e.preventDefault();
  if (ws) return;
  nick = $('nick').value;
  $('notice').hidden = true;
  connect();
});

$('respawn').addEventListener('click', () => send({ t: 'join', name: nick }));

window.addEventListener('resize', resize);
resize();
requestAnimationFrame(frame);
