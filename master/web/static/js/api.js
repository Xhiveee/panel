// api.js — fetch wrapper, base64 helpers, formatters, toasts.

async function api(path, opts = {}) {
  const init = {
    method: opts.method || 'GET',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
  };
  if (opts.body !== undefined) init.body = JSON.stringify(opts.body);
  const res = await fetch(path, init);
  // Only bounce to the login page when the session is actually gone. The
  // login page itself and the /auth/me probe legitimately get 401s, so they
  // must not trigger a redirect (that would reload the page in a loop).
  if (res.status === 401 &&
      !path.startsWith('/api/auth/login') &&
      !path.startsWith('/api/auth/me') &&
      !location.pathname.startsWith('/login')) {
    location.href = '/login';
    throw new Error('unauthorized');
  }
  let data = {};
  try { data = await res.json(); } catch (e) { /* empty body */ }
  if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
  return data;
}

async function guard() {
  try {
    const { user } = await api('/api/auth/me');
    return user;
  } catch (e) {
    return null;
  }
}

function toast(msg, kind = 'info') {
  const wrap = document.getElementById('toasts');
  if (!wrap) return;
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.textContent = msg;
  wrap.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

function fmtBytes(n) {
  if (n == null) return '—';
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(1) + ' ' + u[i];
}

function fmtDate(ts) {
  if (!ts) return '—';
  return new Date(ts * 1000).toLocaleString();
}

function fmtUptime(sec) {
  if (!sec || sec < 0) return '—';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return d + 'd ' + h + 'h';
  if (h) return h + 'h ' + m + 'm';
  return m + 'm ' + Math.floor(sec % 60) + 's';
}

// splitCmd parses a command line honoring single/double quotes.
function splitCmd(line) {
  const out = [];
  let cur = '';
  let q = null;
  for (const ch of line.trim()) {
    if (q) {
      if (ch === q) q = null; else cur += ch;
    } else if (ch === '"' || ch === "'") {
      q = ch;
    } else if (ch === ' ' || ch === '\t') {
      if (cur) { out.push(cur); cur = ''; }
    } else cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

// parseEnvText parses "K=V" lines into an env object. Lines without '=' are
// ignored so a stray line cannot silently become a malformed variable.
function parseEnvText(text) {
  const out = {};
  for (const raw of String(text || '').split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    const i = line.indexOf('=');
    if (i <= 0) continue;
    out[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  return out;
}

// envToText renders an env object back into "K=V" lines.
function envToText(env) {
  return Object.entries(env || {}).map(([k, v]) => k + '=' + v).join('\n');
}

function b64encode(str) {
  const bytes = new TextEncoder().encode(str);
  let bin = '';
  bytes.forEach(b => bin += String.fromCharCode(b));
  return btoa(bin);
}

function b64decode(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

async function fileToB64(file) {
  const buf = await file.arrayBuffer();
  const bytes = new Uint8Array(buf);
  let bin = '';
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + CHUNK));
  }
  return btoa(bin);
}

function confirmDlg(msg) { return window.confirm(msg); }
