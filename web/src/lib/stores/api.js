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
export const minPasswordLength = writable(12);

const MAX_EVENTS = 100;

/**
 * Issues a request against the API.
 *
 * Throws on any non-2xx response so callers cannot mistake an error body for
 * data. A 401 additionally clears the authenticated store, which sends the
 * app back to the login screen.
 */
async function apiFetch(path, opts = {}) {
  const res = await fetch(`/api${path}`, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...opts.headers },
  });

  if (res.status === 401) {
    authenticated.set(false);
    const body = await readJSON(res);
    if (body?.setup_required) needsSetup.set(true);
    throw new ApiError(body?.error ?? 'Unauthorized', res.status);
  }

  if (!res.ok) {
    const body = await readJSON(res);
    throw new ApiError(body?.error ?? `Request failed (HTTP ${res.status})`, res.status);
  }

  // 204 and empty bodies are valid responses; don't choke on them.
  return readJSON(res);
}

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function readJSON(res) {
  try {
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  } catch {
    return null;
  }
}

/**
 * Determines which screen to show: first-run setup, login, or the dashboard.
 *
 * This asks the server directly rather than inferring from a failed request.
 * A previous version fell back to the unauthenticated /health endpoint when
 * /status returned 401 and, because /health always succeeds, concluded the
 * user was authenticated — so the login screen was unreachable and the
 * dashboard rendered empty.
 */
export async function checkAuth() {
  try {
    const res = await fetch('/api/auth/status', {
      headers: { 'Content-Type': 'application/json' },
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);

    const info = await res.json();
    needsSetup.set(Boolean(info.setup_required));
    authenticated.set(Boolean(info.authenticated));
    if (typeof info.min_password_length === 'number') {
      minPasswordLength.set(info.min_password_length);
    }
  } catch (e) {
    console.error('auth status check failed:', e);
    needsSetup.set(false);
    authenticated.set(false);
  }
}

/**
 * Authenticates with the admin password.
 * Returns { ok } on success or { ok: false, error } with a server message.
 */
export async function login(password) {
  try {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });

    if (res.ok) {
      authenticated.set(true);
      needsSetup.set(false);
      return { ok: true };
    }

    const body = await readJSON(res);
    return { ok: false, error: body?.error ?? 'Invalid password' };
  } catch (e) {
    return { ok: false, error: 'Could not reach the server' };
  }
}

/** Sets the initial admin password, then logs in with it. */
export async function setup(password) {
  try {
    const res = await fetch('/api/auth/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });

    if (!res.ok) {
      const body = await readJSON(res);
      return { ok: false, error: body?.error ?? 'Setup failed' };
    }

    needsSetup.set(false);
    return login(password);
  } catch (e) {
    return { ok: false, error: 'Could not reach the server' };
  }
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
