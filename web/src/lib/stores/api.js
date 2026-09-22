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
export const passwordPinned = writable(false);
export const configReadonly = writable(false);

/**
 * Whether the first snapshot is still in flight.
 *
 * Without this the dashboard rendered its empty state -- "No facts
 * received. Check source configuration." -- during the perfectly normal
 * gap before the first fetch returns. At 2am that reads as "your config is
 * broken" when the truth is "still loading".
 */
export const loading = writable(true);

/**
 * The reason the last refresh failed, or null.
 *
 * Refresh errors were previously swallowed into console.error, so a daemon
 * that had stopped answering looked identical to one reporting steady
 * numbers -- the worst possible failure mode for a screen whose entire job
 * is telling you what is happening right now.
 */
export const lastError = writable(null);

/** When the last successful refresh landed, for staleness display. */
export const lastUpdated = writable(null);

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
    passwordPinned.set(Boolean(info.password_pinned));
    configReadonly.set(Boolean(info.config_readonly));
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

/**
 * Changes the admin password.
 *
 * The current password is required even though the caller holds a session:
 * an unattended browser must not be able to lock the real operator out.
 * Every session is invalidated on success, including this one.
 */
export async function changePassword(currentPassword, newPassword) {
  try {
    await apiFetch('/auth/password', {
      method: 'POST',
      body: JSON.stringify({
        current_password: currentPassword,
        new_password: newPassword,
      }),
    });
    disconnectWS();
    authenticated.set(false);
    return { ok: true };
  } catch (e) {
    return { ok: false, error: e.message };
  }
}

/** Ends the session server-side and returns to the login screen. */
export async function logout() {
  try {
    await fetch('/api/auth/logout', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    });
  } catch (e) {
    console.error('logout request failed:', e);
  } finally {
    // Clear local state regardless: the cookie may already be gone, and
    // leaving the dashboard up after a logout attempt is worse than
    // returning to the login screen.
    disconnectWS();
    authenticated.set(false);
    status.set(null);
    facts.set({});
    clients.set([]);
    plans.set([]);
    sequence.set(null);
    events.set([]);
    lastError.set(null);
    lastUpdated.set(null);
    loading.set(true);
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
    lastError.set(null);
    lastUpdated.set(Date.now());
  } catch (e) {
    console.error('refresh failed:', e);
    // A 401 is not a fault to report: the session simply ended, and the
    // app is already on its way back to the login screen.
    if (!(e instanceof ApiError && e.status === 401)) {
      lastError.set(e.message ?? 'Could not reach the daemon');
    }
  } finally {
    loading.set(false);
  }
}

export async function setMode(mode) {
  try {
    await apiFetch('/mode', { method: 'POST', body: JSON.stringify({ mode }) });
    await refreshAll();
    return { ok: true };
  } catch (e) {
    await refreshAll();
    return { ok: false, error: e.message };
  }
}

/**
 * Requests that the running sequence be aborted.
 * Returns { ok } or { ok: false, error } — the server refuses with 409 once
 * the point of no return has been crossed.
 */
export async function abortSequence(reason) {
  try {
    await apiFetch('/abort', {
      method: 'POST',
      body: JSON.stringify({ reason: reason ?? 'requested from the web UI' }),
    });
    await refreshAll();
    return { ok: true };
  } catch (e) {
    await refreshAll();
    return { ok: false, error: e.message };
  }
}

/** Forces a held stage to proceed despite its entry condition. */
export async function proceedStage(reason) {
  try {
    await apiFetch('/sequence/proceed', {
      method: 'POST',
      body: JSON.stringify({ reason: reason ?? 'forced from the web UI' }),
    });
    await refreshAll();
    return { ok: true };
  } catch (e) {
    await refreshAll();
    return { ok: false, error: e.message };
  }
}

let ws = null;
let reconnectTimer = null;
let reconnectAttempts = 0;
let refreshTimer = null;

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;

/** Events whose arrival means the cached snapshot is out of date. */
const REFRESH_TRIGGERING_EVENTS = new Set([
  'client_state_changed',
  'mode_changed',
  'trigger',
  'would_trigger',
  'abort',
  'ponr_crossed',
  'stage_start',
  'stage_complete',
  'stage_skipped',
  'stage_held',
  'stage_forced',
  'wake_gate_satisfied',
  'sequence_completed',
  // Wake outcomes change client state and were previously missing, so the
  // dashboard did not refresh as hosts came back.
  'client_wake_success',
  'client_wake_failed',
  'ponr_crossed',
]);

/**
 * Coalesces refreshes triggered by inbound events.
 *
 * A staged shutdown or wake emits a burst of client_state_changed events, and
 * refreshing on each one fired five API requests per event — a thundering
 * herd against the daemon at exactly the moment it is busiest.
 */
function scheduleRefresh() {
  if (refreshTimer) return;
  refreshTimer = setTimeout(() => {
    refreshTimer = null;
    refreshAll();
  }, 250);
}

export function connectWS() {
  if (ws) return;

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/api/ws`);

  ws.onopen = () => {
    connected.set(true);
    reconnectAttempts = 0;
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  };

  ws.onmessage = (e) => {
    let evt;
    try {
      evt = JSON.parse(e.data);
    } catch (err) {
      console.error('discarding malformed event:', err);
      return;
    }

    events.update(list => [evt, ...list].slice(0, MAX_EVENTS));

    if (REFRESH_TRIGGERING_EVENTS.has(evt.type)) {
      scheduleRefresh();
    }
  };

  ws.onclose = () => {
    connected.set(false);
    ws = null;

    // Back off so a daemon that is down does not get hammered, with jitter
    // so several open tabs do not reconnect in lockstep.
    const delay = Math.min(
      RECONNECT_BASE_MS * 2 ** reconnectAttempts,
      RECONNECT_MAX_MS,
    );
    reconnectAttempts += 1;
    reconnectTimer = setTimeout(connectWS, delay + Math.random() * 1000);
  };

  ws.onerror = () => {
    ws?.close();
  };
}

/** Closes the event stream and cancels any pending reconnect. */
export function disconnectWS() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  if (refreshTimer) {
    clearTimeout(refreshTimer);
    refreshTimer = null;
  }
  reconnectAttempts = 0;
  if (ws) {
    // Drop the handler first so onclose does not schedule a reconnect.
    ws.onclose = null;
    ws.close();
    ws = null;
  }
  connected.set(false);
}
