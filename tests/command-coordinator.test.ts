import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { countCalls } from '../src/desktop/call-limits';
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

it('reports failed Stop when pending removal is saved during a manual lookup', async () => {
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
    command: { commandId, testId, status: CommandStatusStatus.CommandStatusStatusFailed },
  }));
  const value = coordinator();
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
  expect(value.getSnapshot().stopMessage).toMatch(/Безопасное давление не подтверждено/);
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
