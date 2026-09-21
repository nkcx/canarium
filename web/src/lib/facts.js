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

/** A unit suffix, unless formatValue has already expressed it. */
export function unitSuffix(fact) {
  if (!fact?.unit) return '';
  if (fact.type === 'percent' || fact.type === 'duration') return '';
  return ` ${fact.unit}`;
}

export function timeAgo(ts) {
  if (!ts) return '';
  const d = typeof ts === 'number' ? new Date(ts) : new Date(ts);
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
export function factRank(key, fact) {
  if (fact?.quality !== 'good') return 0;
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
  return Object.entries(factMap).sort(([ka, a], [kb, b]) => {
    const rank = factRank(ka, a) - factRank(kb, b);
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
