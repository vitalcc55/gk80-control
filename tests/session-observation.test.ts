import { afterEach, expect, it, vi } from 'vitest';
import type { SessionBridge } from '../src/desktop/bridge';
import {
  SessionObservation,
  type SessionProjection,
  sessionObservation,
} from '../src/desktop/session-observation';
import {
  type ConnectionNotice,
  ConnectionStateStatus,
  type SessionView,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

const host = 'host';
const backend = 'backend';
const view: SessionView = {
  connection: {
    hostRunId: host,
    revision: 1,
    status: ConnectionStateStatus.Live,
    linkValid: true,
    leaseAcquireUnknown: false,
    streamGeneration: 0,
  },
  snapshot: {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 1,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  },
  validForMs: 1000,
  linkValidForMs: 1000,
};
afterEach(() => vi.useRealTimers());

it.each(['beginSessionCheck', 'keepAlive', 'sessionView'] as const)(
  'keeps physical %s calls bounded through replacement and disposal',
  async (stage) => {
    vi.useFakeTimers();
    const pending: Array<() => void> = [];
    const wait = () =>
      new Promise<never>((_resolve, reject) => pending.push(() => reject(new Error('finished'))));
    const blocked = vi.fn(wait);
    const bridge = (): SessionBridge => ({
      beginSessionCheck: stage === 'beginSessionCheck' ? blocked : async () => 'ticket',
      keepAlive: stage === 'keepAlive' ? blocked : async () => {},
      sessionView: stage === 'sessionView' ? blocked : async () => view,
    });
    const observer = new SessionObservation(host, backend);
    const old = vi.fn();
    let stop = observer.subscribe(bridge(), old);
    await vi.advanceTimersByTimeAsync(10000);
    for (let i = 0; i < 3; i += 1) {
      stop();
      stop = observer.subscribe(bridge(), () => {});
      await vi.advanceTimersByTimeAsync(10000);
    }
    expect(blocked).toHaveBeenCalledTimes(2);
    const oldCalls = old.mock.calls.length;
    stop();
    for (const finish of pending) finish();
    await vi.advanceTimersByTimeAsync(0);
    expect(old).toHaveBeenCalledTimes(oldCalls);
    const replacement = vi.fn();
    stop = observer.subscribe(bridge(), replacement);
    await vi.advanceTimersByTimeAsync(0);
    expect(blocked).toHaveBeenCalledTimes(3);
    stop();
  },
);

it('does not deliver a replaced channel result to either consumer', async () => {
  vi.useFakeTimers();
  let finish: (value: SessionView) => void = () => {};
  const delayed = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => delayed,
  };
  const second: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
  };
  const old: SessionProjection[] = [];
  const current: SessionProjection[] = [];
  const stopOld = observer.subscribe(first, (projection) => old.push(projection));
  await vi.advanceTimersByTimeAsync(0);
  const stopCurrent = observer.subscribe(second, (projection) => current.push(projection));
  await vi.advanceTimersByTimeAsync(0);
  const oldCount = old.length;
  const newCount = current.length;
  finish({ ...view, connection: { ...view.connection, revision: 100 } });
  await vi.advanceTimersByTimeAsync(0);
  expect(old).toHaveLength(oldCount);
  expect(current).toHaveLength(newCount);
  expect(observer.getSnapshot().connection?.view.connection.revision).toBe(1);
  stopOld();
  stopCurrent();
});

it('keeps pending calls and terminal outcome for the same identity after another host', async () => {
  vi.useFakeTimers();
  const observer = sessionObservation(host, backend);
  const never = vi.fn(() => new Promise<string>(() => {}));
  const bridge: SessionBridge = {
    beginSessionCheck: never,
    keepAlive: async () => {},
    sessionView: async () => view,
  };
  let stop = observer.subscribe(bridge, () => {});
  await vi.advanceTimersByTimeAsync(10000);
  stop();
  const other = sessionObservation('other', 'other');
  const stopOther = other.subscribe(bridge, () => {});
  stopOther();
  expect(sessionObservation(host, backend)).toBe(observer);
  stop = observer.subscribe({ ...bridge }, () => {});
  await vi.advanceTimersByTimeAsync(10000);
  expect(never).toHaveBeenCalledTimes(2);
  stop();
});

it('does not restore a snapshot overtaken by an event across wrapper replacement', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  expect(observer.getSnapshot().connection).not.toBeNull();
  notify({
    ...view.connection,
    revision: 2,
    linkValid: false,
    status: ConnectionStateStatus.Disconnected,
  });
  const stopNew = observer.subscribe({ ...first }, () => {});
  await vi.advanceTimersByTimeAsync(0);
  expect(observer.getSnapshot().linkValid).toBe(false);
  expect(observer.getSnapshot().connection).toBeNull();
  stopOld();
  stopNew();
});

it('reports a stream gap before a recovered snapshot after wrapper replacement', async () => {
  vi.useFakeTimers();
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
  };
  const seen: SessionProjection[] = [];
  const stopOld = observer.subscribe(first, (value) => seen.push(value));
  await vi.advanceTimersByTimeAsync(0);
  const initialGap = observer.getSnapshot().gapVersion;
  const stopNew = observer.subscribe(
    {
      ...first,
      sessionView: async () => ({
        ...view,
        connection: { ...view.connection, revision: 2, streamGeneration: 1 },
      }),
    },
    (value) => seen.push(value),
  );
  await vi.advanceTimersByTimeAsync(0);
  const recovered = seen.findIndex(
    (value) => value.connection?.view.connection.streamGeneration === 1,
  );
  expect(recovered).toBeGreaterThan(0);
  expect(seen[recovered - 1]?.gapVersion).toBeGreaterThan(initialGap);
  stopOld();
  stopNew();
});

it('retains a late unknown acquire from the replaced wrapper without accepting its snapshot', async () => {
  vi.useFakeTimers();
  let finish: (value: SessionView) => void = () => {};
  const oldView = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const never = new Promise<SessionView>(() => {});
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => never,
    observeSession: () => oldView,
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  const keepAlive = vi.fn().mockRejectedValue(new Error('unavailable'));
  const stopNew = observer.subscribe(
    { ...first, keepAlive, observeSession: () => never },
    () => {},
  );
  await vi.advanceTimersByTimeAsync(0);
  finish({
    ...view,
    connection: {
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Disconnected,
      linkValid: false,
      leaseAcquireUnknown: true,
    },
  });
  await vi.advanceTimersByTimeAsync(0);
  const calls = keepAlive.mock.calls.length;
  await vi.advanceTimersByTimeAsync(10000);
  expect(observer.getSnapshot().unknownAcquire).toBe(true);
  expect(observer.getSnapshot().connection).toBeNull();
  expect(keepAlive).toHaveBeenCalledTimes(calls);
  stopOld();
  stopNew();
});

it('starts a new passive read immediately when an old wrapper is stuck', async () => {
  vi.useFakeTimers();
  const observer = new SessionObservation(host, backend);
  const never = new Promise<SessionView>(() => {});
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {
      throw new Error('offline');
    },
    sessionView: () => never,
    observeSession: () => never,
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  const observeSession = vi.fn().mockResolvedValue(view);
  const stopNew = observer.subscribe({ ...first, observeSession }, () => {});
  await vi.advanceTimersByTimeAsync(0);
  expect(observeSession).toHaveBeenCalledOnce();
  expect(observer.getSnapshot().lastSnapshot).toEqual(view.snapshot);
  expect(observer.getSnapshot().connection).toBeNull();
  stopOld();
  stopNew();
});

it('uses a free enable slot after a wrapper replacement', async () => {
  vi.useFakeTimers();
  const observer = new SessionObservation(host, backend);
  const never = new Promise<void>(() => {});
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    enableConnectionEvents: () => never,
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  const enableConnectionEvents = vi.fn().mockResolvedValue(undefined);
  const stopNew = observer.subscribe({ ...first, enableConnectionEvents }, () => {});
  await vi.advanceTimersByTimeAsync(2000);
  expect(enableConnectionEvents).toHaveBeenCalledOnce();
  expect(observer.getSnapshot().linkValid).toBe(true);
  stopOld();
  stopNew();
});

it.each(['observeSession', 'waitConnection'] as const)(
  'bounds unresolved %s calls through replacement',
  async (stage) => {
    vi.useFakeTimers();
    const pending: Array<() => void> = [];
    const blocked = vi.fn(
      () =>
        new Promise<never>((_resolve, reject) => pending.push(() => reject(new Error('finished')))),
    );
    const bridge = (): SessionBridge => ({
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => view,
      ...(stage === 'observeSession' ? { observeSession: blocked } : { waitConnection: blocked }),
    });
    const observer = new SessionObservation(host, backend);
    let stop = observer.subscribe(bridge(), () => {});
    await vi.advanceTimersByTimeAsync(10000);
    for (let i = 0; i < 3; i += 1) {
      stop();
      stop = observer.subscribe(bridge(), () => {});
      await vi.advanceTimersByTimeAsync(10000);
    }
    expect(blocked).toHaveBeenCalledTimes(stage === 'observeSession' ? 2 : 3);
    stop();
    for (const finish of pending) finish();
    await vi.advanceTimersByTimeAsync(0);
    stop = observer.subscribe(bridge(), () => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(blocked).toHaveBeenCalledTimes(stage === 'observeSession' ? 3 : 4);
    stop();
  },
);

it('subtracts delivery time from both deadlines and expires without events', async () => {
  vi.useFakeTimers();
  let finish: (value: SessionView) => void = () => {};
  const delayed = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const observer = new SessionObservation(host, backend);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => delayed,
  };
  const stop = observer.subscribe(bridge, () => {});
  await vi.advanceTimersByTimeAsync(900);
  finish(view);
  await vi.advanceTimersByTimeAsync(0);
  expect(observer.getSnapshot().linkValid).toBe(true);
  expect(observer.getSnapshot().connection).not.toBeNull();
  await vi.advanceTimersByTimeAsync(100);
  expect(observer.getSnapshot().linkValid).toBe(false);
  expect(observer.getSnapshot().connection).toBeNull();
  expect(observer.getSnapshot().lastSnapshot).toEqual(view.snapshot);
  stop();
});

it('limits physical calls when both identity and wrapper change', async () => {
  vi.useFakeTimers();
  const begin = vi.fn(() => new Promise<string>(() => {}));
  const bridge = (): SessionBridge => ({
    beginSessionCheck: begin,
    keepAlive: async () => {},
    sessionView: async () => view,
  });
  for (const identity of ['a', 'b', 'c']) {
    const observer = sessionObservation(identity, identity);
    const stop = observer.subscribe(bridge(), () => {});
    await vi.advanceTimersByTimeAsync(10000);
    stop();
  }
  expect(begin).toHaveBeenCalledTimes(2);
});

it('never exposes a confirmed connection under a new wrapper before subscription', async () => {
  vi.useFakeTimers();
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
  };
  const stop = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  expect(observer.getSnapshotFor(first).connection).not.toBeNull();
  const next = { ...first };
  expect(observer.getSnapshotFor(next).connection).toBeNull();
  expect(observer.getSnapshotFor(next).linkValid).toBe(false);
  expect(observer.getSnapshotFor(next).lastSnapshot).toEqual(view.snapshot);
  stop();
});

it('retains a late unknown from sessionView after replacing the wrapper', async () => {
  vi.useFakeTimers();
  let finish: (value: SessionView) => void = () => {};
  const delayed = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const never = new Promise<SessionView>(() => {});
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => delayed,
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  const keepAlive = vi.fn().mockRejectedValue(new Error('offline'));
  const stopNew = observer.subscribe({ ...first, keepAlive, sessionView: () => never }, () => {});
  await vi.advanceTimersByTimeAsync(0);
  finish({
    ...view,
    connection: {
      ...view.connection,
      revision: 2,
      leaseAcquireUnknown: true,
      linkValid: false,
      status: ConnectionStateStatus.Disconnected,
    },
  });
  await vi.advanceTimersByTimeAsync(0);
  expect(observer.getSnapshot().unknownAcquire).toBe(true);
  const calls = keepAlive.mock.calls.length;
  await vi.advanceTimersByTimeAsync(10000);
  expect(keepAlive).toHaveBeenCalledTimes(calls);
  stopOld();
  stopNew();
});

it('marks a gap when a replacement remains unconfirmed past the former link deadline', async () => {
  vi.useFakeTimers();
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  const initialGap = observer.getSnapshot().gapVersion;
  const never = new Promise<SessionView>(() => {});
  const stopNew = observer.subscribe({ ...first, sessionView: () => never }, () => {});
  await vi.advanceTimersByTimeAsync(1000);
  expect(observer.getSnapshot().gapVersion).toBeGreaterThan(initialGap);
  stopOld();
  stopNew();
});

it('records an expired link before recovery even if its timer is delayed', async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, 'now').mockReturnValue(0);
  const observer = new SessionObservation(host, backend);
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
  };
  const stopOld = observer.subscribe(first, () => {});
  await vi.advanceTimersByTimeAsync(0);
  const gap = observer.getSnapshot().gapVersion;
  let finish: (value: SessionView) => void = () => {};
  const pending = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const seen: SessionProjection[] = [];
  const stopNew = observer.subscribe({ ...first, sessionView: () => pending }, (value) =>
    seen.push(value),
  );
  await vi.advanceTimersByTimeAsync(0);
  clock.mockReturnValue(1001);
  expect(observer.getSnapshot().linkValid).toBe(false);
  finish({
    ...view,
    validForMs: 6000,
    linkValidForMs: 6000,
    connection: { ...view.connection, revision: 2 },
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(observer.getSnapshot().gapVersion).toBeGreaterThan(gap);
  const recovered = seen.findIndex((value) => value.connection?.view.connection.revision === 2);
  expect(recovered).toBeGreaterThan(0);
  expect(seen[recovered - 1]?.gapVersion).toBeGreaterThan(gap);
  stopOld();
  stopNew();
  clock.mockRestore();
});

it('bounds unfinished acknowledgements and sends the latest exact acknowledgement when a slot frees', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  const pending: Array<() => void> = [];
  const acknowledgeConnection = vi.fn(() => new Promise<void>((resolve) => pending.push(resolve)));
  const observer = new SessionObservation(host, backend);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    acknowledgeConnection,
  };
  const stop = observer.subscribe(bridge, () => {});
  await vi.advanceTimersByTimeAsync(0);
  for (let revision = 2; revision <= 20; revision += 1) notify({ ...view.connection, revision });
  await vi.advanceTimersByTimeAsync(0);
  expect(acknowledgeConnection).toHaveBeenCalledTimes(2);
  notify({ ...view.connection, revision: 0 });
  pending[0]?.();
  await vi.advanceTimersByTimeAsync(0);
  expect(acknowledgeConnection).toHaveBeenCalledTimes(3);
  expect(acknowledgeConnection).toHaveBeenLastCalledWith(host, 20);
  stop();
});

it('marks a timed out fallback wait before accepting a delayed notice', async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, 'now').mockReturnValue(0);
  const observer = new SessionObservation(host, backend);
  let finish: (value: ConnectionNotice) => void = () => {};
  const delayed = new Promise<ConnectionNotice>((resolve) => {
    finish = resolve;
  });
  const notice: ConnectionNotice = {
    connection: view.connection,
    event: null,
    gap: false,
    snapshotBoundary: 0,
  };
  const waitConnection = vi.fn().mockResolvedValueOnce(notice).mockReturnValue(delayed);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({ ...view, validForMs: 10000, linkValidForMs: 10000 }),
    waitConnection,
  };
  const seen: SessionProjection[] = [];
  const stop = observer.subscribe(bridge, (value) => seen.push(value));
  await vi.advanceTimersByTimeAsync(500);
  const gap = observer.getSnapshot().gapVersion;
  clock.mockReturnValue(5001);
  finish(notice);
  for (let i = 0; i < 12; i += 1) await Promise.resolve();
  expect(observer.getSnapshot().gapVersion).toBeGreaterThan(gap);
  stop();
  clock.mockRestore();
});

it('preserves a queued exact acknowledgement across remount until a slot settles', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  const pending: Array<() => void> = [];
  const acknowledgeConnection = vi.fn(() => new Promise<void>((resolve) => pending.push(resolve)));
  const observer = new SessionObservation(host, backend);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    acknowledgeConnection,
  };
  const stop = observer.subscribe(bridge, () => {});
  await vi.advanceTimersByTimeAsync(0);
  for (let revision = 2; revision <= 4; revision += 1) notify({ ...view.connection, revision });
  expect(acknowledgeConnection).toHaveBeenCalledTimes(2);
  stop();
  const replacement = { ...bridge };
  const stopNew = observer.subscribe(replacement, () => {});
  pending[0]?.();
  await vi.advanceTimersByTimeAsync(0);
  expect(acknowledgeConnection).toHaveBeenCalledTimes(3);
  expect(acknowledgeConnection).toHaveBeenLastCalledWith(host, 4);
  stopNew();
});

it('does not send heartbeat after the check deadline even when its timer is delayed', async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, 'now').mockReturnValue(0);
  let finish: (value: string) => void = () => {};
  const ticket = new Promise<string>((resolve) => {
    finish = resolve;
  });
  const keepAlive = vi.fn().mockResolvedValue(undefined);
  const observer = new SessionObservation(host, backend);
  const stop = observer.subscribe(
    { beginSessionCheck: () => ticket, keepAlive, sessionView: async () => view },
    () => {},
  );
  clock.mockReturnValue(4001);
  finish('ticket');
  for (let i = 0; i < 10; i += 1) await Promise.resolve();
  expect(keepAlive).not.toHaveBeenCalled();
  expect(observer.getSnapshot().connection).toBeNull();
  stop();
  clock.mockRestore();
});

it('does not restore freshness from a notice delivered after the wait deadline', async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, 'now').mockReturnValue(0);
  const observer = new SessionObservation(host, backend);
  let finishNotice: (value: ConnectionNotice) => void = () => {};
  let finishView: (value: SessionView) => void = () => {};
  const delayedNotice = new Promise<ConnectionNotice>((resolve) => {
    finishNotice = resolve;
  });
  const delayedView = new Promise<SessionView>((resolve) => {
    finishView = resolve;
  });
  const notice: ConnectionNotice = {
    connection: view.connection,
    event: null,
    gap: false,
    snapshotBoundary: 0,
  };
  const waitConnection = vi.fn().mockResolvedValueOnce(notice).mockReturnValue(delayedNotice);
  const sessionView = vi
    .fn()
    .mockResolvedValueOnce({ ...view, validForMs: 10000, linkValidForMs: 10000 })
    .mockReturnValue(delayedView);
  const stop = observer.subscribe(
    {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView,
      waitConnection,
    },
    () => {},
  );
  await vi.advanceTimersByTimeAsync(2000);
  clock.mockReturnValue(5001);
  finishNotice(notice);
  finishView({ ...view, validForMs: 10000, linkValidForMs: 10000 });
  for (let i = 0; i < 15; i += 1) await Promise.resolve();
  expect(observer.getSnapshot().linkValid).toBe(false);
  expect(observer.getSnapshot().connection).toBeNull();
  stop();
  clock.mockRestore();
});

it('retains a late unknown from the current wrapper after the session check deadline', async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, 'now').mockReturnValue(0);
  let finish: (value: SessionView) => void = () => {};
  const delayed = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const keepAlive = vi.fn().mockResolvedValue(undefined);
  const observer = new SessionObservation(host, backend);
  const stop = observer.subscribe(
    { beginSessionCheck: async () => 'ticket', keepAlive, sessionView: () => delayed },
    () => {},
  );
  await vi.advanceTimersByTimeAsync(0);
  clock.mockReturnValue(4001);
  finish({
    ...view,
    connection: {
      ...view.connection,
      revision: 2,
      linkValid: false,
      leaseAcquireUnknown: true,
      status: ConnectionStateStatus.Disconnected,
    },
  });
  for (let i = 0; i < 10; i += 1) await Promise.resolve();
  expect(observer.getSnapshot().unknownAcquire).toBe(true);
  expect(observer.getSnapshot().connection).toBeNull();
  const calls = keepAlive.mock.calls.length;
  await vi.advanceTimersByTimeAsync(10000);
  expect(keepAlive).toHaveBeenCalledTimes(calls);
  stop();
  clock.mockRestore();
});

it('preserves a queued acknowledgement while a wrapper lacks the ACK method', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  const pending: Array<() => void> = [];
  const acknowledgeConnection = vi.fn(() => new Promise<void>((resolve) => pending.push(resolve)));
  const observer = new SessionObservation(host, backend);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    acknowledgeConnection,
  };
  const stop = observer.subscribe(bridge, () => {});
  await vi.advanceTimersByTimeAsync(0);
  for (let revision = 2; revision <= 4; revision += 1) notify({ ...view.connection, revision });
  stop();
  const withoutAck: SessionBridge = {
    beginSessionCheck: bridge.beginSessionCheck,
    keepAlive: bridge.keepAlive,
    sessionView: bridge.sessionView,
  };
  const stopWithout = observer.subscribe(withoutAck, () => {});
  pending[0]?.();
  await vi.advanceTimersByTimeAsync(0);
  expect(acknowledgeConnection).toHaveBeenCalledTimes(2);
  stopWithout();
  const stopReplacement = observer.subscribe({ ...bridge }, () => {});
  await vi.advanceTimersByTimeAsync(0);
  expect(acknowledgeConnection).toHaveBeenLastCalledWith(host, 4);
  expect(acknowledgeConnection).toHaveBeenCalledTimes(3);
  stopReplacement();
});
