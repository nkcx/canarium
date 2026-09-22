/**
 * Condition explanations from /api/plans, rendered for people.
 *
 * The Plans page used to show a plan's name, its stage count, and the note
 * "plan details are read from the configuration file". An operator
 * wondering why a plan had or had not triggered had to go and read YAML --
 * during the outage.
 */
import { formatSeconds } from './facts.js';

/** A condition as a phrase: "rack_ups.status contains OB". */
export function describeCondition(ex) {
  if (!ex) return 'no condition';

  const fact = ex.fact || '?';

  switch (ex.condition) {
    case 'true': return 'always';
    case 'false': return 'never';
    case 'and': return 'all of';
    case 'or': return 'any of';
    case 'not': return 'none of';
    case 'template': return ex.value || 'an empty expression';

    case 'numeric': {
      const has = (v) => v !== null && v !== undefined;
      if (has(ex.above) && has(ex.below)) return `${fact} between ${ex.above} and ${ex.below}`;
      if (has(ex.below)) return `${fact} below ${ex.below}`;
      if (has(ex.above)) return `${fact} above ${ex.above}`;
      if (has(ex.equals)) return `${fact} equals ${ex.equals}`;
      return `${fact} (no comparison)`;
    }

    case 'state': {
      if (ex.contains) return `${fact} contains ${ex.contains}`;
      if (ex.is) return `${fact} is ${ex.is}`;
      if (ex.is_not) return `${fact} is not ${ex.is_not}`;
      if (ex.in?.length) return `${fact} is one of ${ex.in.join(', ')}`;
      if (ex.equals !== undefined && ex.equals !== null) return `${fact} is ${ex.equals}`;
      return `${fact} (no comparison)`;
    }

    default:
      return ex.condition || 'unknown condition';
  }
}

/** True for conditions that are only a grouping of their children. */
export function isGroup(ex) {
  return ['and', 'or', 'not'].includes(ex?.condition);
}

/**
 * True when a condition holds right now but has not yet held for its
 * `for:`. Reported as "does not hold" alone, this read as a contradiction
 * next to "currently OL" for an abort condition that tests for OL.
 */
export function awaitingDwell(ex) {
  return ex?.result === 'false' && ex?.instant === 'true' && Boolean(ex?.dwell);
}

/** Tone for a result, on the colour contract in app.css. */
export function resultTone(result, ex) {
  if (result === 'true') return 'ok';
  if (awaitingDwell(ex)) return 'live';
  if (result === 'false') return 'neutral';
  return 'warn';
}

/**
 * A result, said in words.
 *
 * "unavailable" is not "false". Under three-valued logic it means a fact
 * the condition needs is not reporting, so the condition cannot be
 * satisfied at all -- which is worth knowing about a trigger, and is the
 * whole reason the fact-quality work exists.
 */
export function resultLabel(result, ex) {
  if (result === 'true') return 'holds';
  if (awaitingDwell(ex)) return 'holds now, not yet for long enough';
  if (result === 'false') return 'does not hold';
  return 'cannot be evaluated';
}

/** The reading a leaf condition decided on: "currently OL". */
export function currentReading(ex) {
  if (!ex || !ex.fact || isGroup(ex)) return '';
  if (ex.fact_quality && ex.fact_quality !== 'good') {
    return `${ex.fact} is ${ex.fact_quality}`;
  }
  const v = ex.fact_value;
  if (v === null || v === undefined) return '';
  if (Array.isArray(v)) return `currently ${v.length ? v.join(' ') : 'empty'}`;
  if (typeof v === 'number') return `currently ${Number.isInteger(v) ? v : v.toFixed(1)}`;
  return `currently ${v}`;
}

/**
 * Dwell progress: "held 12s of 30s".
 *
 * An untracked dwell is said so rather than shown as zero -- a stage entry
 * condition is only timed while a sequence is running, so "0s of 5m" would
 * suggest the condition has been false when nothing is measuring it.
 */
export function dwellText(ex) {
  const d = ex?.dwell;
  if (!d) return '';
  const required = formatSeconds(d.required_seconds);

  if (ex.instant !== 'true') return `needs ${required} continuously`;
  if (!d.tracked) return `needs ${required} continuously; not being timed right now`;
  if (d.elapsed_seconds >= d.required_seconds) return `held for ${required}`;
  return `held ${formatSeconds(d.elapsed_seconds)} of ${required}`;
}
