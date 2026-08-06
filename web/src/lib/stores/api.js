import { writable } from 'svelte/store';

export const status = writable(null);
export const facts = writable({});
export const clients = writable([]);
export const plans = writable([]);
export const sequence = writable(null);
export const connected = writable(false);
export const events = writable([]);
export const authenticated = writable(false);
export const needsSetup = writable(false);

const MAX_EVENTS = 100;

async function apiFetch(path, opts = {}) {
  const res = await fetch(`/api${path}`, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...opts.headers },
  });
  if (res.status === 401) {
    authenticated.set(false);
    throw new Error('Unauthorized');
  }
  return res.json();
}

export async function checkAuth() {
  try {
    await apiFetch('/status');
    authenticated.set(true);
  } catch {
    try {
      const health = await apiFetch('/health');
      if (health.status === 'ok') {
        authenticated.set(true);
      }
    } catch {
      authenticated.set(false);
    }
  }
}

export async function login(password) {
  const res = await fetch('/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
  });
  if (res.ok) {
    authenticated.set(true);
    return true;
  }
  return false;
}

export async function setup(password) {
  const res = await fetch('/api/auth/setup', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
  });
  if (res.ok) {
    needsSetup.set(false);
    return login(password);
  }
  return false;
}

export async function refreshAll() {
  try {
    const [s, f, c, p, seq] = await Promise.all([
      apiFetch('/status'),
      apiFetch('/facts'),
      apiFetch('/clients'),
      apiFetch('/plans'),
      apiFetch('/sequence'),
    ]);
    status.set(s);
    facts.set(f || {});
    clients.set(c || []);
    plans.set(p || []);
    sequence.set(seq);
  } catch (e) {
    console.error('refresh failed:', e);
  }
}

export async function setMode(mode) {
  await apiFetch('/mode', {
    method: 'POST',
    body: JSON.stringify({ mode }),
  });
  await refreshAll();
}

export async function abortSequence() {
  await apiFetch('/abort', { method: 'POST' });
  await refreshAll();
}

let ws = null;
let reconnectTimer = null;

export function connectWS() {
  if (ws) return;

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/api/ws`);

  ws.onopen = () => {
    connected.set(true);
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  };

  ws.onmessage = (e) => {
    try {
      const evt = JSON.parse(e.data);
      events.update(list => {
        const next = [evt, ...list];
        return next.slice(0, MAX_EVENTS);
      });

      if (evt.type === 'client_state_changed' || evt.type === 'mode_changed') {
        refreshAll();
      }
    } catch {}
  };

  ws.onclose = () => {
    connected.set(false);
    ws = null;
    reconnectTimer = setTimeout(connectWS, 5000);
  };

  ws.onerror = () => {
    ws?.close();
  };
}
