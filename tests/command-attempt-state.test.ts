import { describe, expect, it } from 'vitest';
import {
  mergePending,
  mergeStatus,
  type Pending,
  parsePending,
  samePending,
} from '../src/desktop/command-attempt-state';
import { CommandStatusStatus } from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

const attempt: Pending = {
  id: '67829755-cf60-4aee-bf0c-090bbba39dd8',
  kind: 'stop',
  testId: 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa',
  actionId: null,
  originalJSON: '{"commandId":"67829755-cf60-4aee-bf0c-090bbba39dd8"}',
  status: null,
  retired: false,
  receiptRequired: true,
  legacyUnverifiable: false,
  legacyReviewed: false,
};

describe('command attempt identity and recovery', () => {
  it('keeps exact bytes and each part of identity, independently of observation metadata', () => {
    expect(samePending(attempt, { ...attempt, retired: true })).toBe(true);
    for (const changed of [
      { ...attempt, originalJSON: ` ${attempt.originalJSON}` },
      { ...attempt, testId: 'another-test' },
      { ...attempt, actionId: 'another-action' },
      { ...attempt, kind: 'start' as const },
    ])
      expect(samePending(attempt, changed)).toBe(false);
  });

  it('never lowers a terminal result or an accepted result to unknown', () => {
    const unknown = {
      commandId: attempt.id,
      status: CommandStatusStatus.CommandStatusStatusUnknown,
    };
    for (const status of [
      CommandStatusStatus.CommandStatusStatusAccepted,
      CommandStatusStatus.CommandStatusStatusCompleted,
      CommandStatusStatus.CommandStatusStatusFailed,
      CommandStatusStatus.CommandStatusStatusRejected,
    ]) {
      const confirmed = { commandId: attempt.id, testId: attempt.testId, status };
      expect(mergeStatus(confirmed, unknown)).toBe(confirmed);
    }
  });

  it('keeps both a volatile attempt and a new shared attempt without matching only by ID', () => {
    const other = { ...attempt, originalJSON: ` ${attempt.originalJSON}` };
    expect(mergePending([other], [attempt], [], [])).toEqual([other, attempt]);
  });

  it('honors a shared removal but retains an explicitly reactivated observation', () => {
    expect(mergePending([], [attempt], [attempt], [])).toEqual([]);
    expect(mergePending([], [attempt], [attempt], [attempt])).toEqual([attempt]);
  });

  it('requires a receipt after reading schema 2 and preserves legacy uncertainty', () => {
    const restored = parsePending({ ...attempt, schema: 2, receiptRequired: false });
    expect(restored?.originalJSON).toBe(attempt.originalJSON);
    expect(restored?.receiptRequired).toBe(true);
    expect(restored?.status).toBeNull();
    const legacy = parsePending({
      id: attempt.id,
      kind: 'start',
      testId: attempt.testId,
      retired: true,
    });
    expect(legacy?.legacyUnverifiable).toBe(true);
    expect(legacy?.retired).toBe(false);
  });
});
