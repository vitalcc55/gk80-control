import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { countCalls } from '../src/desktop/call-limits';
import { ordinaryNoticeKey } from '../src/desktop/command-attempt-state';
import {
  type CommandCoordinator,
  type CommandInput,
  createCommandCoordinator,
} from '../src/desktop/command-coordinator';
import { CommandConflictError, type DemoService } from '../src/desktop/demo-service';
import {
  CommandDispatchDelivery,
  CommandStatusStatus,
  ConnectionStateStatus,
  type ExactLookupResult,
  ExactLookupResultOutcome,
  type Snapshot,
  TestParametersDirection,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

async function unexpected(): Promise<never> {
  throw new Error('unexpected service call');
}
function service(overrides: Partial<DemoService> = {}): DemoService {
  return {
    createTest: unexpected,
    startTest: unexpected,
    confirmAction: unexpected,
    stopTest: unexpected,
    lookupCommandExact: unexpected,
    command: unexpected,
    readiness: unexpected,
    listTests: unexpected,
    test: unexpected,
    measurements: unexpected,
    audit: unexpected,
    ...overrides,
  };
}
const coordinators: CommandCoordinator[] = [];
let databaseId = '';
let storageKey = '';
const commandId = '986c2358-26df-4b09-bba3-1feaa576d488';
const parameters = { valve: 'К-1', area: 0.012, pmax: 10000, measurementSeconds: 60 };
const unknown: ExactLookupResult = {
  outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeUnknown,
  command: null,
};
async function flush() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}
function input(api: DemoService): CommandInput {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: 'backend',
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
    measurements: [],
  };
  const connection = {
    view: {
      connection: {
        hostRunId: 'host',
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    },
    expiresAt: 6000,
  };
  return {
    service: api,
    connection,
    lastSnapshot: snapshot,
    isCurrentConnection: (value) => value === connection,
  };
}
function coordinator() {
  const value = createCommandCoordinator({
    hostRunId: 'host',
    backendRunId: 'backend',
    databaseId,
    pendingStorage: localStorage,
  });
  coordinators.push(value);
  return value;
}
function seed() {
  localStorage.setItem(
    storageKey,
    JSON.stringify([
      {
        id: commandId,
        kind: 'create',
        testId: null,
        retired: false,
        schema: 2,
        originalJSON: JSON.stringify({ commandId }),
        actionId: null,
      },
    ]),
  );
}
beforeEach(() => {
  databaseId = crypto.randomUUID();
  storageKey = `gk80.demo.pending-command.v1:${databaseId}`;
  localStorage.clear();
  sessionStorage.clear();
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
});
afterEach(() => {
  for (const value of coordinators.splice(0)) value.dispose();
  vi.useRealTimers();
});

it('constructs and reads a stable snapshot without storage or transport effects', () => {
  const read = vi.spyOn(Storage.prototype, 'getItem');
  const value = coordinator();
  expect(value.getSnapshot()).toBe(value.getSnapshot());
  expect(value.getSnapshot().blocking).toBe(true);
  expect(read).not.toHaveBeenCalled();
  read.mockRestore();
});

it.each([false, true])('keeps notices for distinct attempts when removed elsewhere=%s', async (removedElsewhere) => {
  const first = JSON.stringify({ commandId, expectedRevision: 1 });
  const second = JSON.stringify({ commandId, expectedRevision: 2 });
  localStorage.setItem(storageKey, JSON.stringify([first, second].map((originalJSON) => ({
    schema: 2, id: commandId, kind: 'create', testId: null,
    actionId: null, originalJSON, retired: true,
  }))));
  const lookup = vi.fn<DemoService['lookupCommandExact']>(async (_db, _kind, _test, _action, id, originalJSON) =>
    originalJSON === first
      ? unknown
      : { outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch, command: {
          commandId: id, testId: 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa',
          status: CommandStatusStatus.CommandStatusStatusAccepted,
        } },
  );
  const value = coordinator();
  value.update(input(service({ lookupCommandExact: lookup })));
  value.start();
  await flush();
  const attempts = value.getSnapshot().pending;
  const a = attempts.find((item) => item.originalJSON === first);
  const b = attempts.find((item) => item.originalJSON === second);
  if (!a || !b) throw new Error('Attempts missing');
  await value.lookup(a, true);
  await flush();
  await value.lookup(b, true);
  await flush();
  expect(value.getSnapshot().commandMessages.get(ordinaryNoticeKey(a))?.text)
    .toBe('Исход команды пока неизвестен. Автоматический повтор не выполняется.');
  expect(value.getSnapshot().commandMessages.get(ordinaryNoticeKey(b))?.text)
    .toBe('Команда принята или её исход ещё не подтверждён.');
  const finishLookups: Array<(value: ExactLookupResult) => void> = [];
  lookup.mockImplementation(() => new Promise((resolve) => { finishLookups.push(resolve); }));
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.forget(a);
  await flush();
  failure.mockRestore();
  if (removedElsewhere) {
    localStorage.setItem(storageKey, JSON.stringify([{
      schema: 2, id: commandId, kind: 'create', testId: null,
      actionId: null, originalJSON: second, retired: true,
    }]));
  }
  value.retryStorage();
  await flush();
  expect(value.getSnapshot().commandMessages.has(ordinaryNoticeKey(a))).toBe(false);
  expect(value.getSnapshot().commandMessages.get(ordinaryNoticeKey(b))?.text)
    .toBe('Команда принята или её исход ещё не подтверждён.');
  expect(value.getSnapshot().pending.map((item) => item.originalJSON)).toEqual([second]);
  expect(JSON.parse(localStorage.getItem(storageKey) ?? '[]').map((item: { originalJSON: string }) => item.originalJSON))
    .toEqual([second]);
  value.dispose();
  finishLookups.forEach((finish) => finish(unknown));
  await flush();
});

it('does not restore a notice after a delayed lookup confirms a removed attempt', async () => {
  const originalJSON = JSON.stringify({ commandId, expectedRevision: 1 });
  localStorage.setItem(storageKey, JSON.stringify([{
    schema: 2, id: commandId, kind: 'create', testId: null,
    actionId: null, originalJSON, retired: true,
  }]));
  let finish: (value: ExactLookupResult) => void = () => {};
  const delayed = new Promise<ExactLookupResult>((resolve) => { finish = resolve; });
  const lookup = vi.fn<DemoService['lookupCommandExact']>(() => delayed);
  const value = coordinator();
  value.update(input(service({ lookupCommandExact: lookup })));
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Attempt missing');
  expect(lookup).toHaveBeenCalledTimes(1);
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.forget(item);
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(1);
  expect(value.getSnapshot().storageUnconfirmed).toBe(true);
  failure.mockRestore();
  finish({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch, command: {
    commandId, testId: 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa',
    status: CommandStatusStatus.CommandStatusStatusAccepted,
  } });
  await delayed;
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(0);
  expect(value.getSnapshot().commandMessages.has(ordinaryNoticeKey(item))).toBe(false);
});

it.each(['retry', 'restart', 'failed-retry'] as const)('removes a notice when another participant removed the attempt during %s', async (recovery) => {
  const originalJSON = JSON.stringify({ commandId, expectedRevision: 1 });
  localStorage.setItem(storageKey, JSON.stringify([{
    schema: 2, id: commandId, kind: 'create', testId: null,
    actionId: null, originalJSON, retired: true,
  }]));
  const value = coordinator();
  value.update({ ...input(service({ lookupCommandExact: async () => unknown })), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Attempt missing');
  await value.lookup(item, true);
  await flush();
  expect(value.getSnapshot().commandMessages.get(ordinaryNoticeKey(item))).toBeDefined();
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.retryStorage();
  await flush();
  expect(value.getSnapshot().storageUnconfirmed).toBe(true);
  failure.mockRestore();
  localStorage.setItem(storageKey, '[]');
  if (recovery === 'restart') {
    value.dispose();
    value.start();
  } else if (recovery === 'failed-retry') {
    const again = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
    value.retryStorage();
    again.mockRestore();
  } else value.retryStorage();
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(0);
  expect(value.getSnapshot().commandMessages.has(ordinaryNoticeKey(item))).toBe(false);
});

it.each([
  [CommandStatusStatus.CommandStatusStatusFailed, /Безопасное давление не подтверждено/],
  [CommandStatusStatus.CommandStatusStatusCompleted, /Остановка подтверждена/],
  [CommandStatusStatus.CommandStatusStatusAccepted, /завершение.*не подтверждено/],
])('reports a manually checked Stop with status %s when removal is saved during lookup', async (status, expected) => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  const originalJSON = JSON.stringify({ commandId });
  localStorage.setItem(storageKey, JSON.stringify([{
    schema: 2, id: commandId, kind: 'stop', testId,
    actionId: null, originalJSON, retired: true,
  }]));
  const lookup = vi.fn<DemoService['lookupCommandExact']>(async () => ({
    outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
    command: { commandId, testId, status },
  }));
  const value = coordinator();
  value.update({ ...input(service({ lookupCommandExact: lookup })), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Stop observation missing');
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.forget(item);
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(1);
  expect(value.getSnapshot().storageUnconfirmed).toBe(true);
  failure.mockRestore();
  await value.lookup(item, true);
  await flush();
  expect(lookup).toHaveBeenCalledTimes(1);
  expect(value.getSnapshot().pending).toHaveLength(0);
  expect([...value.getSnapshot().commandMessages.values()].some((notice) => expected.test(notice.text))).toBe(true);
});

it.each(['failed', 'unknown', 'conflict', 'transport'] as const)('retains independent manual Stop checks when the second result is %s', async (secondOutcome) => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  const ids = [commandId, '77777777-7777-4777-8777-777777777777'];
  localStorage.setItem(storageKey, JSON.stringify(ids.map((id) => ({
    schema: 2, id, kind: 'stop', testId, actionId: null,
    originalJSON: JSON.stringify({ commandId: id }), retired: true,
  }))));
  const replies = new Map<string, { resolve: (value: ExactLookupResult) => void; reject: (reason: Error) => void }>();
  const lookup = vi.fn<DemoService['lookupCommandExact']>((_db, _kind, _test, _action, id) =>
    new Promise((resolve, reject) => { replies.set(id, { resolve, reject }); }));
  const value = coordinator();
  value.update({ ...input(service({ lookupCommandExact: lookup })), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const attempts = value.getSnapshot().pending;
  expect(attempts).toHaveLength(2);
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  attempts.forEach((item) => value.forget(item));
  await flush();
  failure.mockRestore();
  const calls = attempts.map((item) => value.lookup(item, true));
  for (const id of ids) {
    const reply = replies.get(id);
    if (!reply) throw new Error('Stop lookup not started');
    if (id === ids[1] && secondOutcome === 'transport') reply.reject(new Error('unavailable'));
    else if (id === ids[1] && secondOutcome === 'unknown') reply.resolve(unknown);
    else if (id === ids[1] && secondOutcome === 'conflict')
      reply.resolve({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeConflict, command: null });
    else reply.resolve({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch, command: {
      commandId: id, testId, status: CommandStatusStatus.CommandStatusStatusFailed,
    } });
    await flush();
  }
  await Promise.all(calls);
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(0);
  const manualNotices = [...value.getSnapshot().commandMessages.values()].filter((notice) => notice.category === 'manual');
  expect(manualNotices.map((notice) => notice.id).sort()).toEqual(ids.slice().sort());
  expect(manualNotices.find((notice) => notice.id === ids[1])?.text).toMatch(
    secondOutcome === 'conflict'
      ? /сверка невозможна.*Безопасное давление не подтверждено/i
      : secondOutcome === 'failed'
        ? /Остановка не выполнена или отклонена/i
        : /Исход остановки не подтверждён/i,
  );
  const first = [...value.getSnapshot().commandMessages].find(([, notice]) => notice.id === ids[0]);
  if (!first) throw new Error('Manual result missing');
  value.dismissCommandMessage(first[0]);
  await flush();
  expect([...value.getSnapshot().commandMessages.values()].map((notice) => notice.id)).toContain(ids[1]);
  expect([...value.getSnapshot().commandMessages.values()].map((notice) => notice.id)).not.toContain(ids[0]);
});

it('updates a detached manual Stop result when a later receipt confirms the same Stop', async () => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  localStorage.setItem(storageKey, JSON.stringify([{
    schema: 2, id: commandId, kind: 'stop', testId, actionId: null,
    originalJSON: JSON.stringify({ commandId }), retired: true,
  }]));
  const api = service({
    lookupCommandExact: async () => ({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
      command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusAccepted } }),
    stopTest: async () => ({ delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
      command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusCompleted } }),
  });
  const value = coordinator();
  value.update({ ...input(api), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Stop observation missing');
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.forget(item);
  await flush();
  failure.mockRestore();
  await value.lookup(item, true);
  await flush();
  const projection = input(api);
  if (!projection.lastSnapshot) throw new Error('Snapshot missing');
  value.update({ ...projection, connection: null, lastSnapshot: { ...projection.lastSnapshot, activeTest: {
    testId, stopCommandId: commandId, createdAt: '2026-11-03T00:00:00Z', operatorId: 'demo', demo: true,
    stage: TestRecordStage.Running, outcome: TestRecordOutcome.TestRecordOutcomePending,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityPending, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
    parameters: { valve: 'К-1', areaM2: 0.012, pmaxPa: 10000, measurementSeconds: 60,
      direction: TestParametersDirection.TestParametersDirectionPressure, actuator: false },
  } } });
  await value.stop();
  await flush();
  const manual = [...value.getSnapshot().commandMessages.values()].find((notice) => notice.category === 'manual');
  expect(manual?.text).toMatch(/Остановка подтверждена/);
});

it('updates a detached manual result when the same ordinary command is later confirmed', async () => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  const saved = { schema: 2, id: commandId, kind: 'create', testId: null,
    actionId: null, originalJSON: JSON.stringify({ commandId }), retired: true };
  localStorage.setItem(storageKey, JSON.stringify([saved]));
  const lookup = vi.fn<DemoService['lookupCommandExact']>()
    .mockResolvedValueOnce({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
      command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusAccepted } })
    .mockResolvedValueOnce({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
      command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusCompleted } });
  const value = coordinator();
  value.update({ ...input(service({ lookupCommandExact: lookup })), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Attempt missing');
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.forget(item);
  await flush();
  failure.mockRestore();
  await value.lookup(item, true);
  await flush();
  localStorage.setItem(storageKey, JSON.stringify([saved]));
  value.dispose();
  value.start();
  await flush();
  const restored = value.getSnapshot().pending[0];
  if (!restored) throw new Error('Restored attempt missing');
  await value.lookup(restored);
  await flush();
  const manual = [...value.getSnapshot().commandMessages.values()].find((notice) => notice.category === 'manual');
  expect(manual?.text).toMatch(/Команда выполнена/);
});

it.each(['accepted', 'unknown'] as const)('updates a %s manual result when a delayed ordinary receipt arrives', async (initialResult) => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  let finish: (value: Awaited<ReturnType<DemoService['createTest']>>) => void = () => {};
  let finishLookup: (value: ExactLookupResult) => void = () => {};
  const api = service({
    createTest: () => new Promise((resolve) => { finish = resolve; }),
    lookupCommandExact: async (_db, _kind, _test, _action, id) => initialResult === 'unknown'
      ? new Promise((resolve) => { finishLookup = resolve; })
      : ({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
          command: { commandId: id, testId, status: CommandStatusStatus.CommandStatusStatusAccepted } }),
  });
  const value = coordinator();
  value.update(input(api));
  value.start();
  await flush();
  const call = value.create(parameters);
  value.update({ ...input(api), connection: null, lastSnapshot: null });
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Attempt missing');
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.retryStorage();
  await flush();
  failure.mockRestore();
  localStorage.setItem(storageKey, '[]');
  const lookup = value.lookup(item, true);
  if (initialResult === 'unknown') {
    value.retryStorage();
    await flush();
    finishLookup(unknown);
  }
  await lookup;
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(0);
  finish({ delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
    command: { commandId: item.id, testId, status: CommandStatusStatus.CommandStatusStatusCompleted } });
  await call;
  await flush();
  const manual = [...value.getSnapshot().commandMessages.values()].find((notice) => notice.category === 'manual');
  expect(manual?.text).toMatch(/Команда выполнена/);
  expect(manual?.testId).toBe(testId);
});

it('reserves space for ongoing manual checks and results awaiting dismissal', async () => {
  const ids = Array.from({ length: 65 }, () => crypto.randomUUID());
  localStorage.setItem(storageKey, JSON.stringify(ids.map((id) => ({
    schema: 2, id, kind: id === ids[64] ? 'stop' : 'create',
    testId: id === ids[64] ? 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa' : null, actionId: null,
    originalJSON: JSON.stringify({ commandId: id }), retired: true,
  }))));
  const replies: Array<(value: ExactLookupResult) => void> = [];
  const lookup = vi.fn<DemoService['lookupCommandExact']>(() =>
    new Promise((resolve) => { replies.push(resolve); }));
  const stop = vi.fn<DemoService['stopTest']>(async () => ({ delivery: CommandDispatchDelivery.CommandDispatchDeliveryNotSent, command: null }));
  const api = service({ lookupCommandExact: lookup, stopTest: stop });
  const value = coordinator();
  value.update({ ...input(api), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const first = value.getSnapshot().pending[0];
  if (!first) throw new Error('Attempt missing');
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.forget(first);
  await flush();
  failure.mockRestore();
  const initial = value.lookup(first, true);
  replies.shift()?.({ outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch, command: {
    commandId: first.id, testId: 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa',
    status: CommandStatusStatus.CommandStatusStatusCompleted,
  } });
  await initial;
  await flush();
  const rest = value.getSnapshot().pending;
  expect(rest).toHaveLength(64);
  const calls = rest.map((item) => value.lookup(item, true));
  await flush();
  const started = lookup.mock.calls.length;
  const message = value.getSnapshot().message;
  const projection = input(api);
  if (!projection.lastSnapshot) throw new Error('Snapshot missing');
  value.update({ ...projection, connection: null, lastSnapshot: { ...projection.lastSnapshot, activeTest: {
    testId: 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa', stopCommandId: crypto.randomUUID(),
    createdAt: '2026-11-03T00:00:00Z', operatorId: 'demo', demo: true,
    stage: TestRecordStage.Running, outcome: TestRecordOutcome.TestRecordOutcomePending,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityPending, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
    parameters: { valve: 'К-1', areaM2: 0.012, pmaxPa: 10000, measurementSeconds: 60,
      direction: TestParametersDirection.TestParametersDirectionPressure, actuator: false },
  } } });
  await value.stop();
  expect(stop).toHaveBeenCalledOnce();
  value.dispose();
  value.start();
  await flush();
  const stopped = value.getSnapshot().pending.find((item) => item.id === ids[64]);
  if (!stopped) throw new Error('Stop attempt missing');
  const resumed = value.lookup(stopped, true);
  const resumedStarted = lookup.mock.calls.length;
  value.dispose();
  replies.forEach((resolve) => resolve(unknown));
  await Promise.all([...calls, resumed]);
  expect(started).toBe(64);
  expect(resumedStarted).toBe(65);
  expect(message).toMatch(/закройте.*сообщени/i);
});

it('does not release a new manual reservation when an older generation settles', async () => {
  seed();
  const replies: Array<(value: ExactLookupResult) => void> = [];
  const lookup = vi.fn<DemoService['lookupCommandExact']>(() => new Promise((resolve) => { replies.push(resolve); }));
  const value = coordinator();
  value.update({ ...input(service({ lookupCommandExact: lookup })), connection: null, lastSnapshot: null });
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Attempt missing');
  const first = value.lookup(item, true);
  value.dispose();
  value.start();
  await flush();
  const second = value.lookup(item, true);
  replies[0]?.(unknown);
  await first;
  await flush();
  await value.lookup(item, true);
  expect(lookup).toHaveBeenCalledTimes(2);
  value.dispose();
  replies[1]?.(unknown);
  await second;
});

it.each([false, true])('keeps a direct Stop conflict accessible after observation removal (another test: %s)', async (anotherTest) => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  let rejectStop: (error: Error) => void = () => {};
  const api = service({
    stopTest: () => new Promise((_resolve, reject) => { rejectStop = reject; }),
    lookupCommandExact: async () => unknown,
  });
  const projection = input(api);
  if (!projection.lastSnapshot) throw new Error('Snapshot missing');
  const activeTest: NonNullable<Snapshot['activeTest']> = {
    testId, stopCommandId: commandId, createdAt: '2026-11-03T00:00:00Z', operatorId: 'demo', demo: true,
    stage: TestRecordStage.Running, outcome: TestRecordOutcome.TestRecordOutcomePending,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityPending, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
    parameters: { valve: 'К-1', areaM2: 0.012, pmaxPa: 10000, measurementSeconds: 60,
      direction: TestParametersDirection.TestParametersDirectionPressure, actuator: false },
  };
  const value = coordinator();
  value.update({ ...projection, connection: null, lastSnapshot: { ...projection.lastSnapshot, activeTest } });
  value.start();
  await flush();
  const call = value.stop();
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(1);
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable'); });
  value.retryStorage();
  await flush();
  failure.mockRestore();
  localStorage.setItem(storageKey, '[]');
  if (anotherTest) {
    value.retryStorage();
    await flush();
    expect(value.getSnapshot().pending).toHaveLength(0);
    value.update({ ...projection, connection: null, lastSnapshot: { ...projection.lastSnapshot,
      activeTest: { ...activeTest, testId: crypto.randomUUID(), stopCommandId: crypto.randomUUID() } } });
  }
  rejectStop(new CommandConflictError());
  await call;
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(0);
  const result = [...value.getSnapshot().commandMessages].find(([, notice]) => /Запрос остановки отклонён/.test(notice.text));
  expect(result).toBeDefined();
  if (!result) throw new Error('Stop result missing');
  value.dismissCommandMessage(result[0]);
  await flush();
  expect(value.getSnapshot().commandMessages.has(result[0])).toBe(false);
});

it('keeps public actions inert before start and after dispose', async () => {
  const value = coordinator();
  const reads = vi.spyOn(Storage.prototype, 'getItem');
  const writes = vi.spyOn(Storage.prototype, 'setItem');
  const initial = value.getSnapshot();
  await value.create(parameters);
  await value.startTest({ valveClosed: true, connectionConfirmed: true });
  await value.confirmAction();
  await value.stop();
  value.readLegacy();
  value.reviewLegacy();
  value.retryStorage();
  value.showMessage('inactive');
  expect(reads).not.toHaveBeenCalled();
  expect(writes).not.toHaveBeenCalled();
  expect(value.getSnapshot()).toBe(initial);
  reads.mockRestore();
  writes.mockRestore();
  value.update(input(service()));
  value.start();
  await flush();
  value.dispose();
  const disposed = value.getSnapshot();
  await value.create(parameters);
  await value.startTest({ valveClosed: true, connectionConfirmed: true });
  await value.confirmAction();
  await value.stop();
  value.showMessage('disposed');
  value.reviewLegacy();
  await flush();
  expect(value.getSnapshot()).toBe(disposed);
});
it('persists the exact ordinary envelope before dispatch and never resends on restart', async () => {
  const create = vi.fn<DemoService['createTest']>(async (command) => {
    const stored = JSON.parse(localStorage.getItem(storageKey) ?? '[]');
    expect(stored[0].id).toBe(command.commandId);
    expect(stored[0].originalJSON).toBe(JSON.stringify(command));
    throw new Error('response lost');
  });
  const lookup = vi.fn<DemoService['lookupCommandExact']>(async () => unknown);
  const value = coordinator();
  const projection = input(service({ createTest: create, lookupCommandExact: lookup }));
  value.update(projection);
  value.start();
  await flush();
  await value.create(parameters);
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(1);
  value.dispose();
  value.update(projection);
  value.start();
  await flush();
  expect(create).toHaveBeenCalledTimes(1);
  expect(lookup).toHaveBeenCalled();
});
it('does not dispatch an ordinary command when persistence fails', async () => {
  const create = vi.fn<DemoService['createTest']>(unexpected);
  const value = coordinator();
  value.update(input(service({ createTest: create })));
  value.start();
  await flush();
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  await value.create(parameters);
  await flush();
  expect(create).not.toHaveBeenCalled();
  expect(value.getSnapshot().storageUnconfirmed).toBe(true);
  failure.mockRestore();
});

it('retains the creation notification when its consumer is disposed during publication', async () => {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  const create = vi.fn<DemoService['createTest']>(async (command) => ({
    delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
    command: {
      commandId: command.commandId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
      testId,
    },
  }));
  const projection = input(
    service({ createTest: create, lookupCommandExact: async () => unknown }),
  );
  const value = coordinator();
  value.update(projection);
  value.start();
  await flush();
  await value.create(parameters);
  await flush();
  const base = projection.lastSnapshot;
  if (!base) throw new Error('missing snapshot');
  const updated = {
    ...base,
    revision: 1,
    activeTest: {
      testId,
      stopCommandId: commandId,
      createdAt: '2026-11-03T00:00:00Z',
      operatorId: 'demo',
      demo: true,
      stage: TestRecordStage.Preparing,
      outcome: TestRecordOutcome.TestRecordOutcomePending,
      dataQuality: TestRecordDataQuality.TestRecordDataQualityPending,
      saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
      parameters: {
        valve: 'К-1',
        areaM2: 0.012,
        pmaxPa: 10000,
        measurementSeconds: 60,
        direction: TestParametersDirection.TestParametersDirectionPressure,
        actuator: false,
      },
    },
  };
  const connection = projection.connection;
  if (!connection) throw new Error('missing connection');
  const next = {
    ...projection,
    connection: { ...connection, view: { ...connection.view, snapshot: updated } },
    lastSnapshot: updated,
  };
  const unsubscribe = value.subscribe(() => {
    if (value.getSnapshot().createdVersion === 1) value.dispose();
  });
  value.update(next);
  await flush();
  unsubscribe();
  expect(value.getSnapshot().createdVersion).toBe(1);
  expect(JSON.parse(localStorage.getItem(storageKey) ?? '[]')).toEqual([]);
  value.update(next);
  value.start();
  await flush();
  expect(value.getSnapshot().createdVersion).toBe(1);
  expect(create).toHaveBeenCalledTimes(1);
});

it('does not let a caller retire or forget an unresolved ordinary attempt using stale flags', async () => {
  seed();
  const value = coordinator();
  value.update(input(service({ lookupCommandExact: async () => unknown })));
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('missing seeded attempt');
  const raw = localStorage.getItem(storageKey);
  expect(value.reviewAttempt({ ...item, legacyUnverifiable: true })).toBe(false);
  value.forget({ ...item, retired: true });
  await flush();
  expect(value.canForget({ ...item, retired: true })).toBe(false);
  expect(localStorage.getItem(storageKey)).toBe(raw);
  expect(value.getSnapshot().blocking).toBe(true);
});
it('retains physical lookup slots through dispose, restart and elapsed time, then releases on settle', async () => {
  seed();
  const replies: ((value: ExactLookupResult) => void)[] = [];
  const lookup = vi.fn<DemoService['lookupCommandExact']>(
    () => new Promise((resolve) => replies.push(resolve)),
  );
  const projection = input(service({ lookupCommandExact: lookup }));
  for (let i = 0; i < 5; i++) {
    const value = coordinator();
    value.update(projection);
    value.start();
    await flush();
    value.dispose();
  }
  expect(lookup).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(60000);
  expect(lookup).toHaveBeenCalledTimes(1);
  expect(countCalls('lookup', JSON.stringify(['host', 'backend', databaseId, commandId]))).toBe(1);
  for (const resolve of replies) resolve(unknown);
  await flush();
  expect(countCalls('lookup', JSON.stringify(['host', 'backend', databaseId, commandId]))).toBe(0);
  const next = coordinator();
  next.update(projection);
  next.start();
  await flush();
  expect(lookup).toHaveBeenCalledTimes(2);
  for (const resolve of replies) resolve(unknown);
  await flush();
});
it('ignores a late direct response after dispose and keeps the replacement wrapper result in the same consumer', async () => {
  let finish: (value: Awaited<ReturnType<DemoService['createTest']>>) => void = () => {};
  const create = vi.fn<DemoService['createTest']>(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const api = service({ createTest: create, lookupCommandExact: async () => unknown });
  const projection = input(api);
  const value = coordinator();
  value.update(projection);
  value.start();
  await flush();
  const pending = value.create(parameters);
  await flush();
  value.update({ ...projection, service: service() });
  finish({ delivery: CommandDispatchDelivery.CommandDispatchDeliveryNotSent, command: null });
  await pending;
  await flush();
  expect(value.getSnapshot().pending).toHaveLength(0);
  expect(value.getSnapshot().commandFailure?.text).toMatch(/не отправлена/);
  value.update(projection);
  const late = value.create(parameters);
  await flush();
  value.dispose();
  const raw = localStorage.getItem(storageKey);
  finish({ delivery: CommandDispatchDelivery.CommandDispatchDeliveryNotSent, command: null });
  await late;
  await flush();
  expect(localStorage.getItem(storageKey)).toBe(raw);
});
