import { describe, it, expect } from 'vitest';
import { parsePath, buildPath, titleFor, VIEWS } from './router.js';

describe('parsePath', () => {
  it('maps each view', () => {
    expect(parsePath('/')).toEqual({ view: 'dashboard', param: null });
    expect(parsePath('/plans')).toEqual({ view: 'plans', param: null });
    expect(parsePath('/settings')).toEqual({ view: 'settings', param: null });
    expect(parsePath('/clients')).toEqual({ view: 'clients', param: null });
  });

  it('reads a selected client', () => {
    expect(parsePath('/clients/vanadium')).toEqual({ view: 'clients', param: 'vanadium' });
  });

  it('decodes a name that needed escaping', () => {
    expect(parsePath('/clients/rack%20one').param).toBe('rack one');
  });

  it('survives a malformed escape rather than throwing mid-render', () => {
    expect(() => parsePath('/clients/%E0%A4%A')).not.toThrow();
  });

  it('sends anything unrecognised to the dashboard', () => {
    // A URL is user input: typed, truncated, or left from an older build.
    for (const p of ['/nope', '/api/status', '', null, undefined, '//']) {
      expect(parsePath(p).view).toBe('dashboard');
    }
  });

  it('treats a nonsense client name as a selection that matches nothing', () => {
    // Browsers normalise `..` before sending, but a hand-typed path should
    // still land somewhere harmless: the client list with nothing selected.
    const r = parsePath('/clients/../settings');
    expect(r.view).toBe('clients');
    expect(typeof r.param).toBe('string');
  });

  it('tolerates a trailing slash', () => {
    expect(parsePath('/plans/')).toEqual({ view: 'plans', param: null });
  });
});

describe('buildPath', () => {
  it('round-trips every view', () => {
    for (const view of VIEWS) {
      expect(parsePath(buildPath(view)).view).toBe(view);
    }
  });

  it('round-trips a client name that needs escaping', () => {
    const name = 'rack one/two';
    expect(parsePath(buildPath('clients', name)).param).toBe(name);
  });

  it('keeps the dashboard at the root', () => {
    expect(buildPath('dashboard')).toBe('/');
    expect(buildPath('bogus')).toBe('/');
  });

  it('ignores a parameter on views that take none', () => {
    expect(buildPath('plans', 'x')).toBe('/plans');
  });
});

describe('titleFor', () => {
  it('names the page so history entries are distinguishable', () => {
    expect(titleFor('dashboard', null)).toBe('Canarium');
    expect(titleFor('plans', null)).toBe('Plans — Canarium');
    expect(titleFor('clients', 'vanadium')).toBe('vanadium — Canarium');
  });
});
