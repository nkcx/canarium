import { describe, it, expect } from 'vitest';
import {
  describeCondition, isGroup, resultTone, resultLabel, currentReading, dwellText,
} from './conditions.js';

describe('describeCondition', () => {
  it('says what each leaf condition tests', () => {
    expect(describeCondition({ condition: 'state', fact: 'rack_ups.status', contains: 'OB' }))
      .toBe('rack_ups.status contains OB');
    expect(describeCondition({ condition: 'state', fact: 's', is: 'OL' })).toBe('s is OL');
    expect(describeCondition({ condition: 'state', fact: 's', is_not: 'OL' })).toBe('s is not OL');
    expect(describeCondition({ condition: 'state', fact: 's', in: ['OB', 'LB'] }))
      .toBe('s is one of OB, LB');
    expect(describeCondition({ condition: 'numeric', fact: 'c', below: 50 })).toBe('c below 50');
    expect(describeCondition({ condition: 'numeric', fact: 'c', above: 60 })).toBe('c above 60');
    expect(describeCondition({ condition: 'numeric', fact: 'c', above: 20, below: 30 }))
      .toBe('c between 20 and 30');
  });

  it('does not lose a zero threshold', () => {
    // `if (ex.below)` would drop a legitimate "below 0".
    expect(describeCondition({ condition: 'numeric', fact: 't', below: 0 })).toBe('t below 0');
  });

  it('names groups and literals', () => {
    expect(describeCondition({ condition: 'and' })).toBe('all of');
    expect(describeCondition({ condition: 'or' })).toBe('any of');
    expect(describeCondition({ condition: 'true' })).toBe('always');
    expect(describeCondition({ condition: 'template', value: 'fact("x") > 1' }))
      .toBe('fact("x") > 1');
  });

  it('copes with missing input', () => {
    expect(describeCondition(null)).toBe('no condition');
    expect(describeCondition({ condition: 'numeric', fact: 'x' })).toContain('no comparison');
  });

  it('recognises groups', () => {
    expect(isGroup({ condition: 'or' })).toBe(true);
    expect(isGroup({ condition: 'state' })).toBe(false);
  });
});

describe('results', () => {
  it('keeps "cannot be evaluated" distinct from "does not hold"', () => {
    // Three-valued logic has to survive into the UI: unavailable means a
    // fact the condition needs is not reporting, so it cannot fire at all.
    expect(resultLabel('false')).toBe('does not hold');
    expect(resultLabel('unavailable')).toBe('cannot be evaluated');
    expect(resultTone('unavailable')).toBe('warn');
    expect(resultTone('false')).not.toBe('warn');
    expect(resultLabel('true')).toBe('holds');
    expect(resultTone('true')).toBe('ok');
  });
});

describe('a condition waiting on its dwell', () => {
  // The abort condition "status contains OL, for 60s" read "does not hold"
  // directly beside "currently OL" -- true by the dwell rule, and
  // indistinguishable from a contradiction.
  const waiting = {
    condition: 'state', fact: 'rack_ups.status', contains: 'OL',
    result: 'false', instant: 'true',
    dwell: { required_seconds: 60, elapsed_seconds: 0, tracked: false },
  };

  it('says it holds now but not for long enough yet', () => {
    expect(resultLabel(waiting.result, waiting)).toBe('holds now, not yet for long enough');
    expect(resultTone(waiting.result, waiting)).toBe('live');
  });

  it('does not apply to a condition that simply does not hold', () => {
    const no = { ...waiting, instant: 'false' };
    expect(resultLabel(no.result, no)).toBe('does not hold');
  });

  it('does not apply to a condition without for:', () => {
    const plain = { ...waiting, dwell: undefined, instant: 'false' };
    expect(resultLabel(plain.result, plain)).toBe('does not hold');
  });
});

describe('currentReading', () => {
  it('shows what the condition decided on', () => {
    expect(currentReading({ condition: 'state', fact: 's', fact_value: ['OL'], fact_quality: 'good' }))
      .toBe('currently OL');
    expect(currentReading({ condition: 'numeric', fact: 'c', fact_value: 100, fact_quality: 'good' }))
      .toBe('currently 100');
    expect(currentReading({ condition: 'numeric', fact: 'c', fact_value: 38.44, fact_quality: 'good' }))
      .toBe('currently 38.4');
  });

  it('says when the reading is not trustworthy', () => {
    expect(currentReading({ condition: 'numeric', fact: 'c', fact_value: 90, fact_quality: 'stale' }))
      .toBe('c is stale');
  });

  it('shows a zero reading', () => {
    expect(currentReading({ condition: 'numeric', fact: 'v', fact_value: 0, fact_quality: 'good' }))
      .toBe('currently 0');
  });

  it('says nothing for groups', () => {
    expect(currentReading({ condition: 'and' })).toBe('');
  });
});

describe('dwellText', () => {
  it('reports progress while the timer runs', () => {
    expect(dwellText({
      instant: 'true', dwell: { required_seconds: 30, elapsed_seconds: 12, tracked: true },
    })).toBe('held 12s of 30s');
  });

  it('reports a satisfied dwell', () => {
    expect(dwellText({
      instant: 'true', dwell: { required_seconds: 300, elapsed_seconds: 400, tracked: true },
    })).toBe('held for 5m 0s');
  });

  it('does not claim zero progress when nothing is timing it', () => {
    const text = dwellText({
      instant: 'true', dwell: { required_seconds: 300, elapsed_seconds: 0, tracked: false },
    });
    expect(text).toContain('not being timed');
    expect(text).not.toContain('0s of');
  });

  it('states the requirement while the condition is false', () => {
    expect(dwellText({
      instant: 'false', dwell: { required_seconds: 30, elapsed_seconds: 0, tracked: true },
    })).toBe('needs 30s continuously');
  });

  it('says nothing for a condition without for:', () => {
    expect(dwellText({ instant: 'true' })).toBe('');
  });
});
