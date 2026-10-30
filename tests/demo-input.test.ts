import { describe, expect, it } from 'vitest';
import { exactPositiveDecimal, trimDemoValve } from '../src/desktop/demo-input';

describe('decimal input for the demo core', () => {
  it.each([
    ['0,10', 0.1],
    ['0001.000', 1],
    ['0.0000001', 0.0000001],
    ['1000000000000000000000', 1e21],
  ])('preserves %s as %s', (input, expected) => {
    expect(exactPositiveDecimal(input)).toEqual({ value: expected });
  });
  it.each(['1.0000000000000001', '9007199254740993', '0', '1e3', '.5', '1.', '-1'])(
    'rejects %s',
    (input) => {
      expect(exactPositiveDecimal(input).value).toBeUndefined();
    },
  );
  it('rejects a duration that would round down into the allowed range', () => {
    expect(exactPositiveDecimal('3600.0000000000001', true).error).toBe('range');
    expect(exactPositiveDecimal('3599.9999999999999', true).error).toBe('precision');
    expect(exactPositiveDecimal('3600.000', true).value).toBe(3600);
  });
  it('recognises the whitespace used by Go for valve names', () => {
    expect(trimDemoValve('\u0085')).toBe('');
    expect(trimDemoValve('\u0085К-1\u0085')).toBe('К-1');
  });
  it('reports unrepresentable positive numbers as precision errors', () => {
    expect(exactPositiveDecimal(`1${'0'.repeat(309)}`).error).toBe('precision');
    expect(exactPositiveDecimal(`0.${'0'.repeat(323)}1`).error).toBe('precision');
  });
});
