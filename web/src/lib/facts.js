/**
 * Fact presentation and ordering.
 *
 * The dashboard previously sorted facts with localeCompare, so on a screen
 * whose whole purpose is "how long do I have", the first card was
 * `client.brick.threatened` — a derived internal — because `c` sorts before
 * `r`. Battery runtime, the number that decides whether there is time to
 * intervene, sat fourth, at the same size and weight as the word "yes".
 */

/** Renders a fact value for display. */
export function formatValue(fact) {
  const val = fact?.value;
  if (val === null || val === undefined) return '—';
  if (Array.isArray(val)) return val.length ? val.join(' ') : '—';
  if (typeof val === 'boolean') return val ? 'yes' : 'no';

  if (typeof val === 'number') {
    if (fact.type === 'duration') return formatSeconds(val);
    if (fact.type === 'percent') return `${round(val)}%`;
    return round(val);
  }

  return String(val);
}

/**
 * Integers stay integers: an earlier version applied toFixed(1) to every
 * number, so a runtime of 3600 seconds read as "3600.0".
 */
function round(n) {
  return Number.isInteger(n) ? String(n) : n.toFixed(1);
}

/** Renders a duration in seconds as something a human reads at a glance. */
export function formatSeconds(seconds) {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const mins = Math.floor(seconds / 60);
  if (mins < 60) return `${mins}m ${Math.round(seconds % 60)}s`;
  return `${Math.floor(mins / 60)}h ${mins % 60}m`;
}

/**
 * Symbols for the units sources declare.
 *
 * NUT declares units as words, so the dashboard rendered "120 volts" next
 * to "76%". An unrecognised unit is shown as declared rather than dropped:
 * a wordy label is better than a number with no unit at all.
 */
const UNIT_SYMBOLS = {
  volts: 'V', volt: 'V', v: 'V',
  amps: 'A', amperes: 'A', amp: 'A', a: 'A',
  watts: 'W', watt: 'W', w: 'W',
  va: 'VA',
  hertz: 'Hz', hz: 'Hz',
  celsius: '°C', c: '°C',
  fahrenheit: '°F', f: '°F',
  seconds: 's', second: 's', s: 's',
  percent: '%',
};

export function unitSymbol(unit) {
  if (!unit) return '';
  return UNIT_SYMBOLS[String(unit).toLowerCase()] ?? unit;
}

/** A unit suffix, unless formatValue has already expressed it. */
export function unitSuffix(fact) {
  if (!fact?.unit) return '';
  if (fact.type === 'percent' || fact.type === 'duration') return '';
  const symbol = unitSymbol(fact.unit);
  // Degrees and percent attach to the number; everything else is spaced.
  return symbol.startsWith('°') || symbol === '%' ? symbol : ` ${symbol}`;
}

/**
 * How long ago something happened, or "never".
 *
 * A fact that has never been reported used to arrive with the zero time,
 * 0001-01-01, which this rendered as "17757122h ago". The API now sends
 * null; anything before the Unix epoch is treated the same way, so an
 * older daemon still renders sensibly.
 */
export function timeAgo(ts) {
  if (ts === null || ts === undefined || ts === '') return 'never';
  const d = new Date(ts);
  if (Number.isNaN(d.getTime()) || d.getTime() <= 0) return 'never';
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 5) return 'just now';
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}

/**
 * Splits a fact key into its source and the reading within it.
 *
 * Fact keys are `source.path.to.reading`, which is longer than a phone
 * column. Truncating produced `rack_ups.battery.ch…` next to
 * `rack_ups.battery.ru…` -- indistinguishable, and they were the charge
 * and the runtime.
 */
export function splitFactKey(key) {
  const dot = key.indexOf('.');
  if (dot < 0) return { source: '', reading: key };
  return { source: key.slice(0, dot), reading: key.slice(dot + 1) };
}

/** True when a fact has never had a value since the daemon started. */
export function neverReported(fact) {
  if (!fact) return true;
  if (fact.value !== null && fact.value !== undefined) return false;
  return timeAgo(fact.updated_at) === 'never';
}

/**
 * Per-source reporting health.
 *
 * The distinction this exists to draw: a UPS that reports its charge but
 * has never reported a temperature does not have a temperature sensor.
 * That is a property of the hardware, not a fault, and it will be true
 * forever. The dashboard counted such facts as "not reporting", so a
 * perfectly healthy BR1500G showed a permanent amber warning about two
 * readings it does not have.
 *
 * A source with no good facts at all is different: that is the source
 * being down, and it is exactly what the warning is for.
 */
export function sourceHealth(factMap) {
  const sources = {};
  for (const [key, fact] of Object.entries(factMap)) {
    const { source } = splitFactKey(key);
    const s = (sources[source] ??= { good: 0, stale: 0, never: 0, lastUpdate: null });
    if (fact?.quality === 'good') s.good += 1;
    else if (neverReported(fact)) s.never += 1;
    else s.stale += 1;

    const t = fact?.updated_at ? new Date(fact.updated_at).getTime() : 0;
    if (t > 0 && (s.lastUpdate === null || t > s.lastUpdate)) s.lastUpdate = t;
  }
  for (const s of Object.values(sources)) {
    s.reporting = s.good > 0;
  }
  return sources;
}

/**
 * Classifies each fact for display.
 *
 *   ok          reporting normally
 *   problem     had a value and lost it, or its whole source is silent --
 *               conditions reading it cannot be satisfied
 *   unsupported never reported, from a source that is otherwise fine --
 *               the device simply does not provide it
 */
export function factStatus(key, fact, health) {
  if (fact?.quality === 'good') return 'ok';
  const { source } = splitFactKey(key);
  if (neverReported(fact) && health?.[source]?.reporting) return 'unsupported';
  return 'problem';
}

export function qualityTone(q) {
  if (q === 'good') return 'ok';
  if (q === 'stale') return 'warn';
  return 'neutral';
}

/**
 * Significance rank: lower sorts first.
 *
 * A fact that stopped reporting outranks everything, because under
 * three-valued logic it cannot satisfy any condition — so it is silently
 * disabling part of the plan, which is the thing most worth knowing and
 * the least visible. Then the power facts that decide the outage. Then
 * everything else. Derived `client.*.threatened` facts sort last: they are
 * machinery, not readings.
 */
export function factRank(key, fact, health) {
  const status = factStatus(key, fact, health);
  if (status === 'problem') return 0;
  // Readings the device does not provide sort after everything, rather
  // than first: they are not a fault, and they will never change.
  if (status === 'unsupported') return 4;
  if (isPower(key)) return 1;
  if (key.startsWith('client.')) return 3;
  return 2;
}

function isPower(key) {
  return (
    key.endsWith('.battery.runtime') ||
    key.endsWith('.battery.charge') ||
    key.endsWith('.status') ||
    key.endsWith('.input.voltage')
  );
}

export function sortFacts(factMap) {
  const health = sourceHealth(factMap);
  return Object.entries(factMap).sort(([ka, a], [kb, b]) => {
    const rank = factRank(ka, a, health) - factRank(kb, b, health);
    if (rank !== 0) return rank;
    return ka.localeCompare(kb);
  });
}

/**
 * Picks the power situation to lead with.
 *
 * With several UPSes the binding constraint is the one with the least
 * runtime left, so that is the one the headline reports. The rest stay
 * visible in the grid below.
 */
export function primaryPower(factMap) {
  const entries = Object.entries(factMap);

  const runtimes = entries.filter(
    ([k, f]) => k.endsWith('.battery.runtime') && typeof f?.value === 'number',
  );

  let source = null;
  let runtime = null;

  for (const [key, fact] of runtimes) {
    if (fact.quality !== 'good') continue;
    if (runtime === null || fact.value < runtime.value) {
      runtime = fact;
      source = key.slice(0, -'.battery.runtime'.length);
    }
  }

  // Nothing usable: fall back to any source that reports a charge, so the
  // headline still says something rather than vanishing.
  if (!source) {
    const charge = entries.find(([k, f]) => k.endsWith('.battery.charge') && f);
    if (charge) source = charge[0].slice(0, -'.battery.charge'.length);
  }
  if (!source) return null;

  const charge = factMap[`${source}.battery.charge`] ?? null;
  const status = factMap[`${source}.status`] ?? null;

  const flags = Array.isArray(status?.value)
    ? status.value
    : typeof status?.value === 'string'
      ? status.value.split(/\s+/)
      : [];

  return {
    source,
    runtime,
    charge,
    status,
    flags,
    // OB is "on battery" in NUT's status vocabulary; OL is "on line".
    onBattery: flags.includes('OB') || flags.includes('DISCHRG'),
    onLine: flags.includes('OL'),
    degraded:
      (runtime && runtime.quality !== 'good') ||
      (charge && charge.quality !== 'good') ||
      (status && status.quality !== 'good'),
  };
}
