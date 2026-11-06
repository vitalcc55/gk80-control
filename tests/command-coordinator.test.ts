import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { countCalls, totalCalls } from '../src/desktop/call-limits';
import { commandLookupKey, memoryFor, pendingKey } from '../src/desktop/command-attempt-state';
import {
  type CommandCoordinator,
  type CommandInput,
  createCommandCoordinator,
} from '../src/desktop/command-coordinator';
import type { DemoService } from '../src/desktop/demo-service';
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
function deferred<T>() {
  let complete: (value: T) => void;
  const promise = new Promise<T>((resolve) => {
    complete = resolve;
  });
  return { promise, resolve: (value: T) => complete(value) };
}
function input(
  api: DemoService,
  activeTest: Snapshot['activeTest'] = null,
  { hostRunId = 'host', backendRunId = 'backend' } = {},
): CommandInput {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId,
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest,
    pendingAction: null,
    measurementTiming: null,
    measurements: [],
  };
  const connection = {
    view: {
      connection: {
        hostRunId,
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
function coordinator(identity: Partial<Parameters<typeof createCommandCoordinator>[0]> = {}) {
  const value = createCommandCoordinator({
    hostRunId: 'host',
    backendRunId: 'backend',
    databaseId,
    pendingStorage: localStorage,
    ...identity,
  });
  coordinators.push(value);
  return value;
}
function observeCoordinator(identity?: Parameters<typeof coordinator>[0]) {
  const NativeMap = globalThis.Map;
  const maps: ReadonlyMap<unknown, unknown>[] = [];
  // Observe the private cache allocated by the real coordinator without changing its API.
  globalThis.Map = class ObservedMap<K, V> extends NativeMap<K, V> {
    constructor(entries?: Iterable<readonly [K, V]> | null) {
      super(entries);
      maps.push(this);
    }
  };
  try {
    return {
      value: coordinator(identity),
      cacheWith: (key: string) => maps.find((map) => map.has(key)),
    };
  } finally {
    globalThis.Map = NativeMap;
  }
}
function runningRecord(): NonNullable<Snapshot['activeTest']> {
  return {
    testId: crypto.randomUUID(),
    stopCommandId: crypto.randomUUID(),
    createdAt: '2026-11-06T00:00:00Z',
    operatorId: 'demo',
    demo: true,
    stage: TestRecordStage.Running,
    outcome: TestRecordOutcome.TestRecordOutcomePending,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityPending,
    saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
    parameters: {
      valve: parameters.valve,
      areaM2: parameters.area,
      pmaxPa: parameters.pmax,
      measurementSeconds: parameters.measurementSeconds,
      direction: TestParametersDirection.TestParametersDirectionPressure,
      actuator: false,
    },
  };
}
function finishedRecord(
  record: ReturnType<typeof runningRecord>,
): ReturnType<typeof runningRecord> {
  return {
    ...record,
    stage: TestRecordStage.Finished,
    outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityIncomplete,
    saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
  };
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

it('releases past terminal Stop results throughout sequential saved tests', async () => {
  const { value, cacheWith } = observeCoordinator();
  const post = vi.fn<DemoService['stopTest']>(async (testId, { commandId }) => ({
    delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
    command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusAccepted },
  }));
  const lookup = vi.fn<DemoService['lookupCommandExact']>(
    async (_, __, testId, ___, commandId) => ({
      outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
      command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusCompleted },
    }),
  );
  const api = service({ stopTest: post, lookupCommandExact: lookup });
  const sizes: number[] = [];
  let stopsFromPastTests = 0;
  let cache: ReturnType<typeof cacheWith>;
  value.start();
  for (let i = 0; i < 128; i++) {
    const record = runningRecord();
    value.update(input(api, record));
    await value.stop();
    await flush();

    const [item] = value.getSnapshot().pending;
    if (!item) throw new Error('Stop observation missing before the test finishes');
    expect(item).toMatchObject({
      testId: record.testId,
      receiptRequired: false,
      status: { status: CommandStatusStatus.CommandStatusStatusCompleted },
    });
    const key = pendingKey(item);
    cache ??= cacheWith(key);
    if (!cache) throw new Error('Stop cache was not observed');
    value.update(input(api, finishedRecord(record)));
    await flush();

    expect(value.getSnapshot().stopMessage).toBe('Остановка подтверждена ядром.');
    expect(value.getSnapshot().pending).toEqual([]);
    expect(localStorage.getItem(storageKey)).toBe('[]');
    expect(memoryFor(databaseId).statuses.size).toBe(0);
    expect(totalCalls('stop')).toBe(0);
    expect(totalCalls('stopLookup')).toBe(0);
    expect(cache.has(key)).toBe(true);
    sizes.push(cache.size);
    stopsFromPastTests = Math.max(stopsFromPastTests, cache.size - 1);
    expect(post).toHaveBeenCalledTimes(i + 1);
    expect(lookup).toHaveBeenCalledTimes(i + 1);
  }
  expect({
    sizesAtCheckpoints: [sizes[0], sizes[63], sizes[64], sizes[127]],
    peakPastResults: stopsFromPastTests,
  }).toEqual({ sizesAtCheckpoints: [1, 1, 1, 1], peakPastResults: 0 });
  value.update(input(api));
  value.dispose();
  await flush();
  expect(cache?.size).toBe(0);
});

it.each(['current', 'previous'] as const)(
  'holds a terminal Stop until a late direct response settles for a %s test',
  async (context) => {
    const { value, cacheWith } = observeCoordinator();
    const direct = deferred<Awaited<ReturnType<DemoService['stopTest']>>>();
    const post = vi.fn<DemoService['stopTest']>(() => direct.promise);
    const a = runningRecord();
    const b = runningRecord();
    const completed = {
      commandId: a.stopCommandId,
      testId: a.testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    };
    const lookup = vi.fn<DemoService['lookupCommandExact']>(async () => ({
      outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
      command: completed,
    }));
    const api = service({ stopTest: post, lookupCommandExact: lookup });
    value.update(input(api, a));
    value.start();
    const sent = value.stop();
    try {
      await flush();
      const [item] = value.getSnapshot().pending;
      if (!item) throw new Error('Stop observation missing');
      const key = pendingKey(item);
      const cache = cacheWith(key);
      expect(item.status).toEqual(completed);
      expect(totalCalls('stopLookup')).toBe(0);

      value.update(input(api, finishedRecord(a)));
      await flush();
      expect(value.getSnapshot().pending).toEqual([]);
      for (const record of [b, finishedRecord(a), context === 'previous' ? b : finishedRecord(a)]) {
        value.update(input(api, record));
        await flush();
        expect(cache?.has(key)).toBe(true);
        expect(countCalls('stop', a.testId)).toBe(1);
      }

      direct.resolve({
        delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
        command: { ...completed, status: CommandStatusStatus.CommandStatusStatusAccepted },
      });
      await sent;
      await flush();
      if (context === 'current') {
        expect(value.getSnapshot().stopMessage).toBe('Остановка подтверждена ядром.');
        expect(cache?.has(key)).toBe(true);
        value.update(input(api, b));
        await flush();
      }
      expect(cache?.has(key)).toBe(false);
      expect(countCalls('stop', a.testId)).toBe(0);
      expect(post).toHaveBeenCalledOnce();
      expect(lookup).toHaveBeenCalledOnce();
    } finally {
      direct.resolve({
        delivery: CommandDispatchDelivery.CommandDispatchDeliveryNotSent,
        command: null,
      });
      await sent;
    }
  },
);

it('does not apply a previous generation Stop response to a restored attempt or another identity', async () => {
  const { value, cacheWith } = observeCoordinator();
  const a = runningRecord();
  const direct = deferred<Awaited<ReturnType<DemoService['stopTest']>>>();
  const post = vi.fn<DemoService['stopTest']>(() => direct.promise);
  const api = service({ stopTest: post, lookupCommandExact: async () => unknown });
  const otherIdentity = {
    hostRunId: 'other-host',
    backendRunId: 'other-backend',
    databaseId: crypto.randomUUID(),
  };
  const other = observeCoordinator(otherIdentity);
  value.update(input(api, a));
  value.start();
  const sent = value.stop();
  try {
    await flush();
    value.dispose();
    other.value.update(input(api, a, otherIdentity));
    other.value.start();
    value.start();
    await flush();
    const [restored] = value.getSnapshot().pending;
    if (!restored) throw new Error('Stop observation was not recovered');
    expect(restored).toMatchObject({ receiptRequired: true, status: null });
    expect(value.getSnapshot().stopCount).toBe(1);
    expect(other.value.getSnapshot().stopCount).toBe(1);

    direct.resolve({
      delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
      command: {
        commandId: a.stopCommandId,
        testId: a.testId,
        status: CommandStatusStatus.CommandStatusStatusCompleted,
      },
    });
    await sent;
    await flush();
    const key = pendingKey(restored);
    expect(value.getSnapshot().pending).toEqual([restored]);
    expect(cacheWith(key)).toBeUndefined();
    expect(other.value.getSnapshot().pending).toEqual([]);
    expect(other.cacheWith(key)).toBeUndefined();
    expect(other.value.getSnapshot().stopMessage).toBe('');
    expect(countCalls('stop', a.testId)).toBe(0);
    expect(post).toHaveBeenCalledOnce();
  } finally {
    direct.resolve({
      delivery: CommandDispatchDelivery.CommandDispatchDeliveryNotSent,
      command: null,
    });
    await sent;
  }
});

it.each(['active', 'disposed'] as const)(
  'keeps a direct terminal Stop through a required receipt and releases it after the last lookup with an %s consumer',
  async (consumer) => {
    const { value, cacheWith } = observeCoordinator();
    const firstLookup = deferred<ExactLookupResult>();
    const lateLookup = deferred<ExactLookupResult>();
    const a = runningRecord();
    const completed = {
      commandId: a.stopCommandId,
      testId: a.testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    };
    const post = vi.fn<DemoService['stopTest']>(async () => ({
      delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
      command: completed,
    }));
    const lookup = vi
      .fn<DemoService['lookupCommandExact']>()
      .mockReturnValueOnce(firstLookup.promise)
      .mockReturnValueOnce(lateLookup.promise)
      .mockResolvedValue({
        outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
        command: completed,
      });
    const api = service({ stopTest: post, lookupCommandExact: lookup });
    value.update(input(api, a));
    value.start();
    try {
      await value.stop();
      await flush();
      const [item] = value.getSnapshot().pending;
      if (!item) throw new Error('Stop observation missing');
      const key = pendingKey(item);
      const cache = cacheWith(key);
      const slot = commandLookupKey('host', 'backend', databaseId, a.stopCommandId);
      expect(item).toMatchObject({ receiptRequired: true, status: completed });
      expect(countCalls('stop', a.testId)).toBe(0);
      expect(countCalls('stopLookup', slot)).toBe(1);

      firstLookup.resolve(unknown);
      await flush();
      expect(value.getSnapshot().pending).toEqual([item]);
      expect(value.getSnapshot().commandMessages.size).toBe(0);
      expect(value.getSnapshot().stopMessage).toBe('Остановка подтверждена ядром.');

      value.update(input(api, finishedRecord(a)));
      await flush();
      expect(value.getSnapshot().pending).toEqual([item]);
      expect(countCalls('stopLookup', slot)).toBe(1);
      await value.lookup(item, true);
      await flush();
      expect(value.getSnapshot().pending).toEqual([]);
      value.update(input(api, runningRecord()));
      await flush();
      expect(cache?.has(key)).toBe(true);
      expect(countCalls('stopLookup', slot)).toBe(1);

      if (consumer === 'disposed') value.dispose();
      lateLookup.resolve(unknown);
      await flush();
      expect(countCalls('stopLookup', slot)).toBe(0);
      expect(cache?.has(key)).toBe(false);
      expect(localStorage.getItem(storageKey)).toBe('[]');
      expect(post).toHaveBeenCalledOnce();
      expect(lookup).toHaveBeenCalledTimes(3);
    } finally {
      firstLookup.resolve(unknown);
      lateLookup.resolve(unknown);
      await flush();
    }
  },
);

it('keeps a terminal Stop for the exact observation during an unreadable Storage restart', async () => {
  const { value, cacheWith } = observeCoordinator();
  const a = runningRecord();
  const rejected = {
    commandId: a.stopCommandId,
    status: CommandStatusStatus.CommandStatusStatusRejected,
  };
  const post = vi.fn<DemoService['stopTest']>(async () => ({
    delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
    command: rejected,
  }));
  const lookup = vi.fn<DemoService['lookupCommandExact']>().mockResolvedValue(unknown);
  const api = service({ stopTest: post, lookupCommandExact: lookup });
  value.update(input(api, a));
  value.start();
  await value.stop();
  await flush();
  const [item] = value.getSnapshot().pending;
  if (!item) throw new Error('Stop observation missing');
  expect(item).toMatchObject({ status: rejected, retired: true });
  const key = pendingKey(item);
  const cache = cacheWith(key);
  value.update(input(api, runningRecord()));
  await flush();
  value.dispose();

  const read = Storage.prototype.getItem;
  const unavailable = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (
    this: Storage,
    key,
  ) {
    if (key === storageKey) throw new Error('unavailable');
    return read.call(this, key);
  });
  try {
    value.start();
    await flush();
    expect(value.getSnapshot().storageUnconfirmed).toBe(true);
    expect(value.getSnapshot().pending).toEqual([]);
    expect(cache?.has(key)).toBe(true);
  } finally {
    unavailable.mockRestore();
  }

  lookup.mockResolvedValue({
    outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
    command: {
      ...rejected,
      testId: a.testId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
    },
  });
  expect(value.retryStorage()).toBe(true);
  await flush();
  const [restored] = value.getSnapshot().pending;
  if (!restored) throw new Error('Stop observation was not recovered');
  expect(restored).toMatchObject({ status: rejected, retired: true, receiptRequired: false });
  expect(pendingKey(restored)).toBe(key);
  value.forget(restored);
  await flush();
  expect(cache?.has(key)).toBe(false);
  expect(localStorage.getItem(storageKey)).toBe('[]');
  expect(post).toHaveBeenCalledOnce();
});

async function checkStopAfterSavedRemoval(status: CommandStatusStatus, message: RegExp) {
  const testId = 'd7dcc7dd-127d-4ba1-81ea-f84c173777fa';
  const originalJSON = JSON.stringify({ commandId });
  localStorage.setItem(
    storageKey,
    JSON.stringify([
      {
        schema: 2,
        id: commandId,
        kind: 'stop',
        testId,
        actionId: null,
        originalJSON,
        retired: true,
      },
    ]),
  );
  const lookup = vi.fn<DemoService['lookupCommandExact']>(async () => ({
    outcome: ExactLookupResultOutcome.ExactLookupResultOutcomeMatch,
    command: { commandId, testId, status },
  }));
  const { value, cacheWith } = observeCoordinator();
  value.update({
    ...input(service({ lookupCommandExact: lookup })),
    connection: null,
    lastSnapshot: null,
  });
  value.start();
  await flush();
  const item = value.getSnapshot().pending[0];
  if (!item) throw new Error('Stop observation missing');
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  try {
    value.forget(item);
    await flush();
    expect(value.getSnapshot().pending).toHaveLength(1);
    expect(value.getSnapshot().storageUnconfirmed).toBe(true);
  } finally {
    failure.mockRestore();
  }
  await value.lookup(item, true);
  await flush();
  expect(lookup).toHaveBeenCalledTimes(1);
  expect(value.getSnapshot().pending).toHaveLength(0);
  expect(value.getSnapshot().stopMessage).toMatch(message);
  expect(cacheWith(pendingKey(item))).toBeUndefined();
  expect(memoryFor(databaseId).statuses.has(pendingKey(item))).toBe(false);
}

it('reports failed Stop when pending removal is saved during a manual lookup', () =>
  checkStopAfterSavedRemoval(
    CommandStatusStatus.CommandStatusStatusFailed,
    /Безопасное давление не подтверждено/,
  ));

it('does not restore a completed Stop after saving its pending removal during a manual lookup', () =>
  checkStopAfterSavedRemoval(CommandStatusStatus.CommandStatusStatusCompleted, /^$/));

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
