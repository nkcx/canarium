import { describe, it, expect } from 'vitest';
import { describeEvent, stateLabel, stateTone, stateNote } from './events.js';

describe('describeEvent', () => {
  it('renders every event type the daemon emits', () => {
    // Kept in step with the emit calls in internal/engine. A type missing
    // here still renders, via the fallback below, but as a generic line
    // rather than a sentence.
    const cases = [
      ['trigger', 'outage', /Plan "outage" triggered/],
      ['would_trigger', 'outage', /Plan "outage" would have triggered — disarmed/],
      ['abort', 'outage', /Sequence "outage" aborted/],
      ['ponr_crossed', 'compute', /Point of no return crossed at stage "compute"/],
      ['stage_start', 'compute', /Stage "compute" started/],
      ['stage_complete', 'compute', /Stage "compute" complete/],
      ['stage_timeout', 'compute', /Stage "compute" timed out/],
      ['stage_held', 'compute', /Stage "compute" is holding/],
      ['wake_gate_satisfied', 'outage', /Wake gate satisfied for "outage"/],
      ['client_wake_success', 'nas', /nas woke successfully/],
      ['client_wake_failed', 'nas', /nas failed to wake/],
      ['mode_changed', 'armed', /Mode changed to armed/],
    ];

    for (const [type, data, expected] of cases) {
      expect(describeEvent({ type, data }).text, type).toMatch(expected);
    }
  });

  it('renders the structured events as sentences, not JSON', () => {
    // The event log used to render `data` through JSON.stringify, so the
    // primary "what just happened" surface showed
    // {"client":"vanadium","state":"shutting_down"}.
    const changed = describeEvent({
      type: 'client_state_changed',
      data: { client: 'vanadium', state: 'shutting_down' },
    });
    expect(changed.text).toBe('vanadium is now shutting down');
    expect(changed.text).not.toContain('{');

    const skipped = describeEvent({
      type: 'stage_skipped',
      data: { stage: 'compute', clients: ['steel', 'vanadium'] },
    });
    expect(skipped.text).toContain('compute');
    expect(skipped.text).toContain('steel and vanadium');
    expect(skipped.text).toContain('left running');

    const done = describeEvent({
      type: 'sequence_completed',
      data: { plan: 'outage', state: 'completed' },
    });
    expect(done.text).toBe('Sequence "outage" finished (completed)');
  });

  it('lists skipped clients readably at each cardinality', () => {
    const one = describeEvent({ type: 'stage_skipped', data: { stage: 's', clients: ['a'] } });
    expect(one.text).toContain('a left running');

    const three = describeEvent({
      type: 'stage_skipped', data: { stage: 's', clients: ['a', 'b', 'c'] },
    });
    expect(three.text).toContain('a, b and c');

    const none = describeEvent({ type: 'stage_skipped', data: { stage: 's', clients: [] } });
    expect(none.text).toContain('no clients');
  });

  it('spends colour only where it means something', () => {
    // Every event type used to be amber, so on a screen with six log rows
    // amber meant nothing.
    expect(describeEvent({ type: 'ponr_crossed', data: 's' }).tone).toBe('danger');
    expect(describeEvent({ type: 'client_wake_failed', data: 'nas' }).tone).toBe('danger');
    expect(describeEvent({ type: 'stage_complete', data: 's' }).tone).toBe('ok');
    expect(describeEvent({ type: 'trigger', data: 'p' }).tone).toBe('live');
    expect(describeEvent({ type: 'stage_held', data: 's' }).tone).toBe('warn');
  });

  it('tones a finished sequence by how it finished', () => {
    expect(describeEvent({
      type: 'sequence_completed', data: { plan: 'p', state: 'completed' },
    }).tone).toBe('ok');
    expect(describeEvent({
      type: 'sequence_completed', data: { plan: 'p', state: 'aborted' },
    }).tone).toBe('warn');
  });

  it('never falls back to raw JSON for an unknown type', () => {
    // A daemon newer than this UI will emit types this build has not seen.
    const e = describeEvent({
      type: 'some_future_event',
      data: { client: 'nas', detail: 'something' },
    });
    expect(e.text).not.toContain('{');
    expect(e.text).not.toContain('"');
    expect(e.text).toContain('Some future event');
    expect(e.text).toContain('nas');
  });

  it('survives malformed events rather than blanking the log', () => {
    expect(() => describeEvent(undefined)).not.toThrow();
    expect(() => describeEvent({})).not.toThrow();
    expect(() => describeEvent({ type: 'trigger' })).not.toThrow();
    expect(() => describeEvent({ type: 'client_state_changed', data: null })).not.toThrow();
    expect(describeEvent({ type: 'client_state_changed', data: {} }).text)
      .toContain('client is now unknown');
  });
});

describe('client state presentation', () => {
  it('says down_unverified in words, and says it is not a failure', () => {
    // Shown as a bare enum next to the same amber dot that means "in
    // progress", an operator reads this as an error. The docs go to
    // lengths to explain that it is not one.
    expect(stateLabel('down_unverified')).toBe('down (unconfirmed)');
    expect(stateNote('down_unverified')).toMatch(/[Nn]ot a failure/);
    expect(stateTone('down_unverified')).not.toBe('danger');
  });

  it('covers every state the engine can report', () => {
    // Mirrors ClientState.String() in internal/engine/types.go.
    for (const s of ['up', 'down', 'down_unverified', 'shutting_down', 'waking', 'failed']) {
      expect(stateLabel(s), s).not.toBe('unknown');
      expect(stateLabel(s), s).not.toContain('_');
    }
    expect(stateLabel('something_else')).toBe('unknown');
  });

  it('reserves danger for actual failure', () => {
    expect(stateTone('failed')).toBe('danger');
    expect(stateTone('up')).toBe('ok');
    expect(stateTone('shutting_down')).toBe('live');
    expect(stateTone('down')).toBe('neutral');
  });

  it('explains only the states that need explaining', () => {
    expect(stateNote('up')).toBe('');
    expect(stateNote('down')).toBe('');
    expect(stateNote('failed')).not.toBe('');
  });
});
