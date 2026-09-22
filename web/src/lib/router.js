import { readable } from 'svelte/store';

/**
 * The address bar is the state of the app.
 *
 * Without this, every view lived at `/`: a reload always landed on the
 * dashboard, the back button left the app entirely, and a link to what you
 * were looking at could not be sent to anyone. For a console read during an
 * outage that last one matters — "look at this" should be a URL.
 *
 * Real paths rather than a hash, because the server already serves
 * index.html for any path the bundle does not contain, so a deep link and a
 * reload both work.
 */

export const VIEWS = ['dashboard', 'clients', 'plans', 'settings'];

/**
 * Parses a pathname into a view and its optional parameter.
 *
 * Anything unrecognised is the dashboard. A URL is user input — typed,
 * truncated, or left over from an older version — and a 404 screen would be
 * a worse answer than the front page.
 */
export function parsePath(pathname) {
  const parts = String(pathname || '/').split('/').filter(Boolean);
  const [head, ...rest] = parts;

  if (!head) return { view: 'dashboard', param: null };
  if (!VIEWS.includes(head)) return { view: 'dashboard', param: null };

  // Only the clients view takes a parameter: /clients/<name>.
  if (head === 'clients' && rest.length > 0) {
    return { view: 'clients', param: safeDecode(rest.join('/')) };
  }
  return { view: head, param: null };
}

function safeDecode(s) {
  try {
    return decodeURIComponent(s);
  } catch {
    // A malformed escape should select nothing, not throw during render.
    return s;
  }
}

/** Builds the path for a view. The dashboard lives at the root. */
export function buildPath(view, param) {
  if (!VIEWS.includes(view)) return '/';
  if (view === 'dashboard') return '/';
  if (view === 'clients' && param) {
    return `/clients/${encodeURIComponent(param)}`;
  }
  return `/${view}`;
}

/** The current route, kept in step with the address bar. */
export const route = readable(currentRoute(), (set) => {
  const update = () => set(currentRoute());
  window.addEventListener('popstate', update);
  window.addEventListener('canarium:navigate', update);
  return () => {
    window.removeEventListener('popstate', update);
    window.removeEventListener('canarium:navigate', update);
  };
});

function currentRoute() {
  if (typeof window === 'undefined') return { view: 'dashboard', param: null };
  return parsePath(window.location.pathname);
}

/**
 * Navigates to a view, adding a history entry.
 *
 * `replace` is for corrections that should not be somewhere the back button
 * can return to — normalising an unknown path, or clearing a selection that
 * no longer exists.
 */
export function navigate(view, param = null, { replace = false } = {}) {
  const path = buildPath(view, param);
  if (path === window.location.pathname) return;

  window.history[replace ? 'replaceState' : 'pushState']({}, '', path);
  // pushState does not fire popstate, so tell the store ourselves.
  window.dispatchEvent(new Event('canarium:navigate'));
}

/**
 * Rewrites the address bar to the canonical path for what is being shown,
 * without adding history. Called once at startup so a typo or a stale link
 * does not leave the bar disagreeing with the screen.
 */
export function normalise() {
  const { view, param } = currentRoute();
  const canonical = buildPath(view, param);
  if (canonical !== window.location.pathname) {
    window.history.replaceState({}, '', canonical);
    window.dispatchEvent(new Event('canarium:navigate'));
  }
}

/** The document title for a route, so history entries are distinguishable. */
export function titleFor(view, param) {
  const name = view.charAt(0).toUpperCase() + view.slice(1);
  if (view === 'clients' && param) return `${param} — Canarium`;
  if (view === 'dashboard') return 'Canarium';
  return `${name} — Canarium`;
}
