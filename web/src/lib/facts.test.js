import { describe, it, expect } from 'vitest';
import {
  formatValue, formatSeconds, unitSuffix, unitSymbol, qualityTone, timeAgo,
  splitFactKey, factRank, sortFacts, primaryPower,
  neverReported, sourceHealth, factStatus,
} from './facts.js';

const good = (value, extra = {}) => ({ value, quality: 'good', ...extra });

describe('formatValue', () => {
  it('leaves integers alone', () => {
    // An earlier version applied toFixed(1) to every number, so a runtime
    // of 3600 seconds read as "3600.0".
    expect(formatValue(good(61, { type: 'percent' }))).toBe('61%');
    expect(formatValue(good(0, { type: 'number' }))).toBe('0');
  });

  it('gives fractions one decimal', () => {
    expect(formatValue(good(38.44, { type: 'percent' }))).toBe('38.4%');
  });

  it('renders durations as time, not a raw count', () => {
    expect(formatValue(good(942, { type: 'duration' }))).toBe('15m 42s');
  });

  it('joins set-valued facts', () => {
    expect(formatValue(good(['OB', 'DISCHRG'], { type: 'set' }))).toBe('OB DISCHRG');
  });

  it('says yes and no rather than true and false', () => {
    expect(formatValue(good(true))).toBe('yes');
    expect(formatValue(good(false))).toBe('no');
  });

  it('renders absent values as a dash rather than undefined', () => {
    expect(formatValue(good(null))).toBe('—');
    expect(formatValue(good(undefined))).toBe('—');
    expect(formatValue(undefined)).toBe('—');
    expect(formatValue(good([]))).toBe('—');
  });

  it('does not mistake a false boolean for a missing value', () => {
    // `if (!val)` would render this as an em dash, which reads as "no
    // data" when the truth is a definite no.
    expect(formatValue(good(false))).not.toBe('—');
  });

  it('does not mistake zero for a missing value', () => {
    // Input voltage of 0 during a mains failure is the single most
    // important reading on the screen.
    expect(formatValue(good(0, { type: 'number' }))).toBe('0');
  });
});

describe('formatSeconds', () => {
  it('covers each magnitude', () => {
    expect(formatSeconds(0)).toBe('0s');
    expect(formatSeconds(59)).toBe('59s');
    expect(formatSeconds(60)).toBe('1m 0s');
    expect(formatSeconds(942)).toBe('15m 42s');
    expect(formatSeconds(3600)).toBe('1h 0m');
    expect(formatSeconds(7860)).toBe('2h 11m');
  });
});

describe('unitSuffix', () => {
  it('does not repeat a unit formatValue already expressed', () => {
    expect(unitSuffix({ unit: '%', type: 'percent' })).toBe('');
    expect(unitSuffix({ unit: 's', type: 'duration' })).toBe('');
  });

  it('appends a unit that would otherwise be lost', () => {
    expect(unitSuffix({ unit: 'V', type: 'number' })).toBe(' V');
  });

  it('uses symbols for the words NUT declares', () => {
    // The dashboard rendered "120 volts" next to "76%".
    expect(unitSuffix({ unit: 'volts', type: 'number' })).toBe(' V');
    expect(unitSuffix({ unit: 'celsius', type: 'number' })).toBe('°C');
    expect(unitSymbol('Watts')).toBe('W');
    expect(unitSymbol('hertz')).toBe('Hz');
  });

  it('shows an unrecognised unit as declared rather than dropping it', () => {
    expect(unitSymbol('furlongs')).toBe('furlongs');
  });

  it('copes with a fact that declares no unit', () => {
    expect(unitSuffix({ type: 'number' })).toBe('');
    expect(unitSuffix(undefined)).toBe('');
  });
});

describe('splitFactKey', () => {
  it('splits source from reading', () => {
    expect(splitFactKey('rack_ups.battery.charge')).toEqual({
      source: 'rack_ups', reading: 'battery.charge',
    });
  });

  it('distinguishes keys that truncation made identical', () => {
    // `rack_ups.battery.ch…` and `rack_ups.battery.ru…` rendered the same
    // at phone width, and they were the charge and the runtime.
    const a = splitFactKey('rack_ups.battery.charge');
    const b = splitFactKey('rack_ups.battery.runtime');
    expect(a.reading).not.toBe(b.reading);
  });

  it('handles a key with no source prefix', () => {
    expect(splitFactKey('standalone')).toEqual({ source: '', reading: 'standalone' });
  });
});

describe('qualityTone', () => {
  it('maps quality onto the colour contract', () => {
    expect(qualityTone('good')).toBe('ok');
    expect(qualityTone('stale')).toBe('warn');
    expect(qualityTone('unknown')).toBe('neutral');
  });
});

describe('factRank / sortFacts', () => {
  it('puts anything not reporting first', () => {
    // A fact that stopped reporting cannot satisfy a condition, so it is
    // silently disabling part of the plan. That outranks everything.
    expect(factRank('temp.rack.celsius', { quality: 'stale' })).toBe(0);
    expect(factRank('rack_ups.battery.runtime', { quality: 'good' })).toBe(1);
  });

  it('ranks power facts above other readings', () => {
    const power = factRank('rack_ups.battery.charge', { quality: 'good' });
    const other = factRank('rack_ups.load', { quality: 'good' });
    expect(power).toBeLessThan(other);
  });

  it('ranks derived client facts last', () => {
    const derived = factRank('client.brick.threatened', { quality: 'good' });
    const other = factRank('rack_ups.load', { quality: 'good' });
    expect(derived).toBeGreaterThan(other);
  });

  it('orders a realistic fact set by significance, not alphabetically', () => {
    // This is the regression the dashboard rebuild exists for: sorted with
    // localeCompare, `client.brick.threatened` led the page because c
    // sorts before r.
    const facts = {
      'client.brick.threatened': good(true),
      'rack_ups.battery.charge': good(38.4),
      'rack_ups.battery.runtime': good(942),
      'rack_ups.load': good(61),
      'temp.rack.celsius': { value: 27.5, quality: 'stale' },
    };

    const keys = sortFacts(facts).map(([k]) => k);

    expect(keys[0]).toBe('temp.rack.celsius');
    expect(keys[keys.length - 1]).toBe('client.brick.threatened');
    expect(keys.indexOf('rack_ups.battery.charge'))
      .toBeLessThan(keys.indexOf('rack_ups.load'));
  });

  it('is stable within a rank so the grid does not reshuffle on refresh', () => {
    // Object.entries follows insertion order, which a Go map randomises
    // per response.
    const facts = {
      'b.load': good(1), 'a.load': good(2), 'c.load': good(3),
    };
    const once = sortFacts(facts).map(([k]) => k);
    const again = sortFacts({ 'c.load': good(3), 'a.load': good(2), 'b.load': good(1) })
      .map(([k]) => k);
    expect(once).toEqual(again);
  });

  it('survives an empty fact set', () => {
    expect(sortFacts({})).toEqual([]);
  });
});

describe('primaryPower', () => {
  const ups = (name, runtime, charge, status, quality = 'good') => ({
    [`${name}.battery.runtime`]: { value: runtime, quality, type: 'duration' },
    [`${name}.battery.charge`]: { value: charge, quality, type: 'percent' },
    [`${name}.status`]: { value: status, quality, type: 'set' },
  });

  it('reports the UPS that is reporting', () => {
    const p = primaryPower(ups('rack_ups', 942, 38.4, ['OB', 'DISCHRG']));
    expect(p.source).toBe('rack_ups');
    expect(p.onBattery).toBe(true);
    expect(p.onLine).toBe(false);
  });

  it('leads with the binding constraint when several UPSes report', () => {
    // The one with the least runtime decides how long there is.
    const facts = {
      ...ups('rack_ups', 942, 38.4, ['OB']),
      ...ups('desk_ups', 300, 20, ['OB']),
    };
    expect(primaryPower(facts).source).toBe('desk_ups');
  });

  it('ignores a runtime it cannot trust when choosing', () => {
    const facts = {
      ...ups('stale_ups', 10, 5, ['OB'], 'stale'),
      ...ups('rack_ups', 942, 38.4, ['OB']),
    };
    // The stale reading claims less runtime but proves nothing, so it must
    // not be what the headline reports.
    expect(primaryPower(facts).source).toBe('rack_ups');
  });

  it('recognises mains power', () => {
    const p = primaryPower(ups('rack_ups', 3600, 100, ['OL']));
    expect(p.onLine).toBe(true);
    expect(p.onBattery).toBe(false);
  });

  it('treats DISCHRG alone as on battery', () => {
    expect(primaryPower(ups('u', 100, 50, ['DISCHRG'])).onBattery).toBe(true);
  });

  it('parses a status reported as a string rather than a list', () => {
    const p = primaryPower({
      'u.battery.runtime': good(100, { type: 'duration' }),
      'u.status': good('OB DISCHRG'),
    });
    expect(p.onBattery).toBe(true);
  });

  it('flags degraded readings so the headline can say so', () => {
    const p = primaryPower({
      'u.battery.runtime': good(942, { type: 'duration' }),
      'u.battery.charge': { value: 38, quality: 'stale' },
    });
    expect(p.degraded).toBe(true);
  });

  it('falls back to a charge-only source rather than vanishing', () => {
    const p = primaryPower({ 'u.battery.charge': good(38.4, { type: 'percent' }) });
    expect(p).not.toBeNull();
    expect(p.source).toBe('u');
    expect(p.runtime).toBeNull();
  });

  it('returns null when nothing describes power at all', () => {
    expect(primaryPower({})).toBeNull();
    expect(primaryPower({ 'temp.rack.celsius': good(27.5) })).toBeNull();
  });
});

describe('timeAgo', () => {
  it('says never for a fact that has never been reported', () => {
    // The zero time rendered as "17757122h ago".
    expect(timeAgo(null)).toBe('never');
    expect(timeAgo(undefined)).toBe('never');
    expect(timeAgo('0001-01-01T00:00:00Z')).toBe('never');
    expect(timeAgo('not a date')).toBe('never');
  });

  it('describes recent times', () => {
    expect(timeAgo(new Date().toISOString())).toBe('just now');
    expect(timeAgo(new Date(Date.now() - 90_000).toISOString())).toBe('1m ago');
  });
});

describe('unsupported readings vs readings that stopped', () => {
  // The exact shape of the lembas deployment: an APC BR1500G that reports
  // charge, runtime and voltages but has no temperature sensor and does
  // not expose output voltage.
  const lembas = {
    'rack_ups.battery.charge': { value: 100, quality: 'good', updated_at: new Date().toISOString() },
    'rack_ups.status': { value: ['OL'], quality: 'good', updated_at: new Date().toISOString() },
    'rack_ups.output.voltage': { value: null, quality: 'unknown', updated_at: null },
    'rack_ups.ups.temperature': { value: null, quality: 'unknown', updated_at: '0001-01-01T00:00:00Z' },
  };

  it('does not call a reading the hardware lacks a problem', () => {
    // A healthy UPS showed a permanent amber "2 not reporting" warning
    // about two readings it does not have.
    const health = sourceHealth(lembas);
    expect(health.rack_ups.reporting).toBe(true);
    expect(factStatus('rack_ups.ups.temperature', lembas['rack_ups.ups.temperature'], health))
      .toBe('unsupported');
    expect(factStatus('rack_ups.output.voltage', lembas['rack_ups.output.voltage'], health))
      .toBe('unsupported');
  });

  it('still calls a reading that went stale a problem', () => {
    const facts = {
      ...lembas,
      'rack_ups.ups.load': { value: 76, quality: 'stale', updated_at: new Date(Date.now() - 600_000).toISOString() },
    };
    const health = sourceHealth(facts);
    expect(factStatus('rack_ups.ups.load', facts['rack_ups.ups.load'], health)).toBe('problem');
  });

  it('calls everything a problem when the whole source is silent', () => {
    // A source that never connected is the failure the warning exists for,
    // and must not be mistaken for a device with few sensors.
    const dead = {
      'rack_ups.battery.charge': { value: null, quality: 'unknown', updated_at: null },
      'rack_ups.status': { value: null, quality: 'unknown', updated_at: null },
    };
    const health = sourceHealth(dead);
    expect(health.rack_ups.reporting).toBe(false);
    expect(factStatus('rack_ups.status', dead['rack_ups.status'], health)).toBe('problem');
  });

  it('sorts unsupported readings to the end, not the front', () => {
    const keys = sortFacts(lembas).map(([k]) => k);
    expect(keys.slice(-2).sort()).toEqual(['rack_ups.output.voltage', 'rack_ups.ups.temperature']);
    expect(keys[0]).not.toBe('rack_ups.output.voltage');
  });

  it('treats a false or zero value as reported', () => {
    expect(neverReported({ value: 0, quality: 'good', updated_at: null })).toBe(false);
    expect(neverReported({ value: false, quality: 'good', updated_at: null })).toBe(false);
  });
});
