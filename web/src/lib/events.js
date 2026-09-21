/**
 * Turns a daemon event into something an operator reads rather than parses.
 *
 * The event log previously rendered `data` through JSON.stringify, so the
 * primary "what just happened" surface showed
 * `{"client":"vanadium","state":"shutting_down"}` — a developer artifact on
 * an operator's screen, at the moment they are least able to decode one.
 */

/** Tones map to the colour contract in app.css. */
const DESCRIPTIONS = {
  trigger: (d) => ({ tone: 'live', text: `Plan "${str(d)}" triggered` }),
  abort: (d) => ({ tone: 'live', text: `Sequence "${str(d)}" aborted` }),
  ponr_crossed: (d) => ({
    tone: 'danger',
    text: `Point of no return crossed at stage "${str(d)}"`,
  }),
  stage_start: (d) => ({ tone: 'live', text: `Stage "${str(d)}" started` }),
  stage_complete: (d) => ({ tone: 'ok', text: `Stage "${str(d)}" complete` }),
  stage_timeout: (d) => ({
    tone: 'warn',
    text: `Stage "${str(d)}" timed out waiting for its entry condition`,
  }),
  stage_held: (d) => ({
    tone: 'warn',
    text: `Stage "${str(d)}" is holding for its entry condition`,
  }),
  stage_skipped: (d) => ({
    tone: 'warn',
    text:
      `Stage "${d?.stage ?? '?'}" skipped — ` +
      `${list(d?.clients)} left running`,
  }),
  wake_gate_satisfied: (d) => ({
    tone: 'ok',
    text: `Wake gate satisfied for "${str(d)}"`,
  }),
  sequence_completed: (d) => ({
    tone: d?.state === 'completed' ? 'ok' : 'warn',
    text: `Sequence "${d?.plan ?? '?'}" finished (${d?.state ?? 'unknown'})`,
  }),
  client_state_changed: (d) => ({
    tone: stateTone(d?.state),
    text: `${d?.client ?? 'client'} is now ${stateLabel(d?.state)}`,
  }),
  client_wake_success: (d) => ({ tone: 'ok', text: `${str(d)} woke successfully` }),
  client_wake_failed: (d) => ({ tone: 'danger', text: `${str(d)} failed to wake` }),
  mode_changed: (d) => ({ tone: 'live', text: `Mode changed to ${str(d)}` }),
};

export function describeEvent(evt) {
  const fn = DESCRIPTIONS[evt?.type];
  if (fn) return fn(evt.data);

  // An event type this build does not know about still has to read as
  // something. Never fall back to raw JSON.
  return { tone: 'neutral', text: `${humanize(evt?.type)}${suffix(evt?.data)}` };
}

function suffix(data) {
  if (data === null || data === undefined || data === '') return '';
  if (typeof data !== 'object') return `: ${data}`;
  const pairs = Object.entries(data).map(([k, v]) => `${k} ${str(v)}`);
  return pairs.length ? `: ${pairs.join(', ')}` : '';
}

function str(v) {
  if (v === null || v === undefined) return '';
  if (Array.isArray(v)) return v.join(', ');
  if (typeof v === 'object') return Object.values(v).join(' ');
  return String(v);
}

function list(v) {
  if (!Array.isArray(v) || v.length === 0) return 'no clients';
  if (v.length === 1) return v[0];
  return `${v.slice(0, -1).join(', ')} and ${v[v.length - 1]}`;
}

function humanize(type) {
  if (!type) return 'Event';
  const s = type.replace(/_/g, ' ');
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/**
 * Client states, said in words.
 *
 * `down_unverified` is the one that matters here. It is not a failure — the
 * shutdown command was sent and the host simply could not be positively
 * confirmed down, which is the normal outcome for a machine on a local
 * subnet whose budget ran short. Shown as a bare enum next to the same
 * amber dot that means "in progress", an operator reads it as an error.
 */
export function stateLabel(state) {
  switch (state) {
    case 'up': return 'up';
    case 'down': return 'down';
    case 'down_unverified': return 'down (unconfirmed)';
    case 'shutting_down': return 'shutting down';
    case 'waking': return 'waking';
    case 'failed': return 'failed';
    default: return 'unknown';
  }
}

export function stateTone(state) {
  switch (state) {
    case 'up': return 'ok';
    case 'down': return 'neutral';
    case 'down_unverified': return 'neutral';
    case 'shutting_down':
    case 'waking': return 'live';
    case 'failed': return 'danger';
    default: return 'neutral';
  }
}

/** The one-line explanation shown under an unusual state. */
export function stateNote(state) {
  if (state === 'down_unverified') {
    return 'Shutdown was sent; the host could not be positively confirmed down. Not a failure.';
  }
  if (state === 'failed') return 'The shutdown command did not succeed.';
  return '';
}
