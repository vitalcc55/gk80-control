import { expect, it } from 'vitest';
import { beginCall, countCalls, endCall } from '../src/desktop/call-limits';

it('bounds lost calls across remounts without blocking another test', () => {
  expect(beginCall('stop', 'A', 2)).toBe(true);
  expect(beginCall('stop', 'A', 2)).toBe(true);
  expect(beginCall('stop', 'A', 2)).toBe(false);
  expect(beginCall('stop', 'B', 2)).toBe(true);
  endCall('stop', 'A');
  endCall('stop', 'A');
  endCall('stop', 'B');
  expect(countCalls('stop', 'A')).toBe(0);
});
