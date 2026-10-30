import { act, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import {
  readLastSession,
  readSession,
  requestSessionObservation,
  type SessionBridge,
  watchConnection,
} from '../src/desktop/bridge';
import { SessionStatus } from '../src/desktop/SessionStatus';
import {
  type ConnectionNotice,
  ConnectionStateStatus,
  type SessionView,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

afterEach(() => vi.useRealTimers());

const view: SessionView = {
  connection: {
    hostRunId: '8d43fc4c-7bf7-408f-a8e8-19d0dfd725c4',
    revision: 1,
    status: ConnectionStateStatus.Live,
    linkValid: true,
    leaseAcquireUnknown: false,
    streamGeneration: 0,
  },
  snapshot: {
    apiVersion: 'v1',
    backendRunId: '6c229f38-3426-4608-bd72-dcd0d3b2bcd7',
    revision: 1,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  },
  validForMs: 1000,
  linkValidForMs: 1000,
};

function call<T>(promise: Promise<T>): Promise<T> {
  return promise;
}

it('reports unknown initial acquisition from a passive view without restoring freshness', async () => {
  vi.useFakeTimers();
  const unknownHost = 'a1111111-1111-4111-8111-111111111111';
  const unknown: SessionView = {
    ...view,
    connection: {
      ...view.connection,
      hostRunId: unknownHost,
      revision: 2,
      status: ConnectionStateStatus.Disconnected,
      linkValid: false,
      leaseAcquireUnknown: true,
    },
    snapshot: null,
    validForMs: 0,
    linkValidForMs: 0,
  };
  let finishObservation: (value: SessionView) => void = () => {};
  const observation = new Promise<SessionView>((resolve) => {
    finishObservation = resolve;
  });
  const keepAlive = vi.fn().mockRejectedValue(new Error('response lost'));
  const onView = vi.fn();
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive,
    sessionView: vi.fn().mockResolvedValue(view),
    observeSession: vi.fn().mockReturnValue(observation),
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={unknownHost}
      backendRunId={view.snapshot?.backendRunId ?? ''}
      onView={onView}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  await act(async () => {
    finishObservation(unknown);
    await observation;
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(screen.getByText('Получение управления не подтверждено')).toBeTruthy();
  expect(screen.getByText(/Запустите снова только после завершения ядра/)).toBeTruthy();
  expect(screen.queryByText('При последней проверке связь с ядром подтверждена')).toBeNull();
  expect(onView.mock.calls.every(([snapshot]) => snapshot === null)).toBe(true);
  expect(keepAlive).toHaveBeenCalledTimes(1);
  expect(bridge.sessionView).not.toHaveBeenCalled();
});

it('keeps the unknown acquisition after a delayed older live event', async () => {
  vi.useFakeTimers();
  const unknownHost = 'b2222222-2222-4222-8222-222222222222';
  let notify: (state: SessionView['connection']) => void = () => {};
  const keepAlive = vi.fn().mockReturnValue(new Promise<void>(() => {}));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive,
    sessionView: vi.fn().mockResolvedValue(view),
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={unknownHost}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  act(() => {
    notify({
      ...view.connection,
      hostRunId: unknownHost,
      revision: 2,
      status: ConnectionStateStatus.Disconnected,
      linkValid: false,
      leaseAcquireUnknown: true,
    });
    notify({ ...view.connection, hostRunId: unknownHost });
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(screen.getByText('Получение управления не подтверждено')).toBeTruthy();
  expect(screen.queryByText('При последней проверке связь с ядром подтверждена')).toBeNull();
  expect(keepAlive).toHaveBeenCalledTimes(1);
});

it('retains the terminal acquisition outcome across bridge replacement and remount', async () => {
  vi.useFakeTimers();
  const unknownHost = 'c3333333-3333-4333-8333-333333333333';
  const backendRunId = view.snapshot?.backendRunId ?? '';
  let notify: (state: SessionView['connection']) => void = () => {};
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: vi.fn().mockReturnValue(new Promise<void>(() => {})),
    sessionView: vi.fn().mockResolvedValue(view),
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  const replacement: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: vi.fn().mockResolvedValue(undefined),
    sessionView: vi.fn().mockResolvedValue(view),
  };
  const mounted = render(
    <SessionStatus bridge={first} hostRunId={unknownHost} backendRunId={backendRunId} />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  act(() => {
    notify({
      ...view.connection,
      hostRunId: unknownHost,
      revision: 2,
      status: ConnectionStateStatus.Disconnected,
      linkValid: false,
      leaseAcquireUnknown: true,
    });
  });
  mounted.rerender(
    <SessionStatus bridge={replacement} hostRunId={unknownHost} backendRunId={backendRunId} />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(screen.getByText('Получение управления не подтверждено')).toBeTruthy();
  expect(replacement.keepAlive).not.toHaveBeenCalled();
  mounted.unmount();
  const remounted = render(
    <SessionStatus bridge={replacement} hostRunId={unknownHost} backendRunId={backendRunId} />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(screen.getByText('Получение управления не подтверждено')).toBeTruthy();
  expect(replacement.keepAlive).not.toHaveBeenCalled();
  const freshHost = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee';
  const fresh: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: vi.fn().mockResolvedValue(undefined),
    sessionView: async () => ({
      ...view,
      connection: { ...view.connection, hostRunId: freshHost },
    }),
  };
  remounted.rerender(
    <SessionStatus bridge={fresh} hostRunId={freshHost} backendRunId={backendRunId} />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.queryByText('Получение управления не подтверждено')).toBeNull();
  expect(fresh.keepAlive).toHaveBeenCalledOnce();
});

it('reports a delayed unknown observation after a newer observation fails', async () => {
  vi.useFakeTimers();
  const unknownHost = 'd4444444-4444-4444-8444-444444444444';
  let finish: (value: SessionView) => void = () => {};
  const delayed = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const never = new Promise<SessionView>(() => {});
  const keepAlive = vi.fn().mockRejectedValue(new Error('unavailable'));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive,
    sessionView: vi.fn().mockResolvedValue(view),
    observeSession: vi
      .fn()
      .mockReturnValueOnce(delayed)
      .mockRejectedValueOnce(new Error('later observation failed'))
      .mockReturnValue(never),
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={unknownHost}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(4001);
  });
  await act(async () => {
    finish({
      ...view,
      connection: {
        ...view.connection,
        hostRunId: unknownHost,
        revision: 2,
        status: ConnectionStateStatus.Disconnected,
        linkValid: false,
        leaseAcquireUnknown: true,
      },
      snapshot: null,
      validForMs: 0,
      linkValidForMs: 0,
    });
    await delayed;
  });
  expect(screen.getByText('Получение управления не подтверждено')).toBeTruthy();
  const attempts = keepAlive.mock.calls.length;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(keepAlive).toHaveBeenCalledTimes(attempts);
});

it('keeps late connection notices from an old host out of the replacement watcher', async () => {
  vi.useFakeTimers();
  let finishOld: (notice: ConnectionNotice) => void = () => {};
  let finishNew: (notice: ConnectionNotice) => void = () => {};
  const oldRequest = new Promise<ConnectionNotice>((resolve) => {
    finishOld = resolve;
  });
  const newRequest = new Promise<ConnectionNotice>((resolve) => {
    finishNew = resolve;
  });
  const waitConnection = vi
    .fn()
    .mockReturnValueOnce(oldRequest)
    .mockReturnValueOnce(newRequest)
    .mockReturnValue(new Promise<ConnectionNotice>(() => {}));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    waitConnection,
  };
  const oldHost = view.connection.hostRunId;
  const newHost = 'e5555555-5555-4555-8555-555555555555';
  const oldBackend = view.snapshot?.backendRunId ?? '';
  const newBackend = 'f6666666-6666-4666-8666-666666666666';
  const notices = vi.fn();
  const stopOld = watchConnection(bridge, oldHost, oldBackend, () => {});
  await vi.advanceTimersByTimeAsync(0);
  stopOld();
  const stopNew = watchConnection(bridge, newHost, newBackend, notices);
  await vi.advanceTimersByTimeAsync(0);
  expect(waitConnection).toHaveBeenCalledTimes(2);
  expect(waitConnection).toHaveBeenNthCalledWith(2, 0, 0);
  await act(async () => {
    finishOld({
      connection: { ...view.connection, revision: 100 },
      event: null,
      gap: false,
      snapshotBoundary: 0,
    });
    await oldRequest;
  });
  expect(notices).not.toHaveBeenCalled();
  await act(async () => {
    finishNew({
      connection: { ...view.connection, hostRunId: newHost, revision: 1 },
      event: null,
      gap: false,
      snapshotBoundary: 0,
    });
    await newRequest;
  });
  expect(notices).toHaveBeenCalledWith(
    expect.objectContaining({
      connection: expect.objectContaining({ hostRunId: newHost, revision: 1 }),
    }),
  );
  stopNew();
});

it('bounds replacement waits and resumes when an old slot is released', async () => {
  vi.useFakeTimers();
  let finishOld: (notice: ConnectionNotice) => void = () => {};
  const oldRequest = new Promise<ConnectionNotice>((resolve) => {
    finishOld = resolve;
  });
  const never = new Promise<ConnectionNotice>(() => {});
  const newHost = 'f7777777-7777-4777-8777-777777777777';
  const newBackend = 'a8888888-8888-4888-8888-888888888888';
  const replacement: ConnectionNotice = {
    connection: { ...view.connection, hostRunId: newHost },
    event: null,
    gap: false,
    snapshotBoundary: 0,
  };
  const waitConnection = vi
    .fn()
    .mockReturnValueOnce(oldRequest)
    .mockReturnValueOnce(never)
    .mockReturnValueOnce(never)
    .mockResolvedValueOnce(replacement)
    .mockReturnValue(never);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    waitConnection,
  };
  const stopOld = watchConnection(
    bridge,
    view.connection.hostRunId,
    view.snapshot?.backendRunId ?? '',
    () => {},
  );
  await vi.advanceTimersByTimeAsync(4001);
  expect(waitConnection).toHaveBeenCalledTimes(2);
  stopOld();
  const notices = vi.fn();
  const stopNew = watchConnection(bridge, newHost, newBackend, notices);
  await vi.advanceTimersByTimeAsync(0);
  expect(waitConnection).toHaveBeenCalledTimes(3);
  await vi.advanceTimersByTimeAsync(4001);
  expect(waitConnection).toHaveBeenCalledTimes(3);
  expect(notices).toHaveBeenCalledWith(null);
  finishOld({ connection: view.connection, event: null, gap: false, snapshotBoundary: 0 });
  await vi.advanceTimersByTimeAsync(500);
  expect(waitConnection).toHaveBeenCalledTimes(4);
  expect(notices).toHaveBeenCalledWith(replacement);
  stopNew();
});

it('delivers a late unknown lease notice after a newer wait fails', async () => {
  vi.useFakeTimers();
  let finish: (notice: ConnectionNotice) => void = () => {};
  const delayed = new Promise<ConnectionNotice>((resolve) => {
    finish = resolve;
  });
  const never = new Promise<ConnectionNotice>(() => {});
  const waitConnection = vi
    .fn()
    .mockReturnValueOnce(delayed)
    .mockRejectedValueOnce(new Error('later wait failed'))
    .mockReturnValue(never);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    waitConnection,
  };
  const notices = vi.fn();
  const stop = watchConnection(
    bridge,
    view.connection.hostRunId,
    view.snapshot?.backendRunId ?? '',
    notices,
  );
  await vi.advanceTimersByTimeAsync(4001);
  expect(waitConnection).toHaveBeenCalledTimes(2);
  await act(async () => {
    finish({
      connection: {
        ...view.connection,
        revision: 2,
        status: ConnectionStateStatus.Disconnected,
        linkValid: false,
        leaseAcquireUnknown: true,
      },
      event: null,
      gap: false,
      snapshotBoundary: 0,
    });
    await delayed;
  });
  expect(notices).toHaveBeenCalledWith(
    expect.objectContaining({
      connection: expect.objectContaining({ leaseAcquireUnknown: true }),
    }),
  );
  stop();
});

it('does not treat a delayed session response as fresh or overlap calls', async () => {
  vi.useFakeTimers();
  let finish: (result: SessionView) => void = () => {};
  const pending = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const bridge: SessionBridge = {
    beginSessionCheck: () => Promise.resolve('ticket'),
    keepAlive: vi.fn(() => call(Promise.resolve())),
    sessionView: vi.fn(() => call(pending)),
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(bridge.keepAlive).toHaveBeenCalledTimes(1);
  await act(async () => {
    finish(view);
    await pending;
  });
  expect(screen.queryByText('При последней проверке связь с ядром подтверждена')).toBeNull();
  mounted.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(bridge.keepAlive).toHaveBeenCalledTimes(1);
});

it('expires a confirmed session instead of assuming the last snapshot stays live', async () => {
  vi.useFakeTimers();
  const never = new Promise<void>(() => {});
  const bridge: SessionBridge = {
    beginSessionCheck: () => Promise.resolve('ticket'),
    keepAlive: vi.fn().mockReturnValueOnce(call(Promise.resolve())).mockReturnValue(call(never)),
    sessionView: vi.fn(() => call(Promise.resolve(view))),
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1001);
  });
  expect(screen.queryByText('При последней проверке связь с ядром подтверждена')).toBeNull();
});

it('clears confirmation immediately when the session identity changes', async () => {
  vi.useFakeTimers();
  const bridge: SessionBridge = {
    beginSessionCheck: () => Promise.resolve('ticket'),
    keepAlive: vi.fn(() => call(Promise.resolve())),
    sessionView: vi.fn(() => call(Promise.resolve(view))),
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  const stalled: SessionBridge = {
    beginSessionCheck: () => Promise.resolve('ticket'),
    keepAlive: () => call(new Promise<void>(() => {})),
    sessionView: bridge.sessionView,
  };
  mounted.rerender(<SessionStatus bridge={stalled} hostRunId="new-host" backendRunId="new-core" />);
  expect(screen.queryByText('При последней проверке связь с ядром подтверждена')).toBeNull();
});

it('shares checks across remounts and bounds unresolved calls', async () => {
  vi.useFakeTimers();
  const stalled = new Promise<void>(() => {});
  const bridge: SessionBridge = {
    beginSessionCheck: () => Promise.resolve('ticket'),
    keepAlive: vi.fn(() => stalled),
    sessionView: vi.fn(() => call(Promise.resolve(view))),
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  mounted.unmount();
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(bridge.keepAlive).toHaveBeenCalledTimes(2);
  expect(bridge.sessionView).not.toHaveBeenCalled();
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
});

it('resumes a waiting session check when an old timed-out call finally settles', async () => {
  vi.useFakeTimers();
  let finishFirst: () => void = () => {};
  const first = new Promise<void>((resolve) => {
    finishFirst = resolve;
  });
  const never = new Promise<void>(() => {});
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: vi
      .fn()
      .mockReturnValueOnce(first)
      .mockReturnValueOnce(never)
      .mockResolvedValue(undefined),
    sessionView: vi.fn(async () => view),
  };
  const results = vi.fn();
  readSession(bridge, results);
  await vi.advanceTimersByTimeAsync(4000);
  readSession(bridge, results);
  await vi.advanceTimersByTimeAsync(4000);
  readSession(bridge, results);
  expect(bridge.keepAlive).toHaveBeenCalledTimes(2);
  finishFirst();
  await vi.advanceTimersByTimeAsync(0);
  expect(bridge.keepAlive).toHaveBeenCalledTimes(3);
  expect(results).toHaveBeenLastCalledWith(expect.objectContaining({ view }));
});

it('recovers last known snapshot after a lost response and another remount', async () => {
  vi.useFakeTimers();
  const never = new Promise<SessionView>(() => {});
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    observeSession: vi.fn().mockReturnValueOnce(never).mockResolvedValue(view),
  };
  const first = vi.fn();
  const unsubscribe = readLastSession(bridge, first);
  await vi.advanceTimersByTimeAsync(4001);
  expect(first).toHaveBeenCalledWith(view, expect.any(Number));
  unsubscribe();
  const second = vi.fn();
  const stop = readLastSession(bridge, second);
  await vi.advanceTimersByTimeAsync(0);
  expect(second).toHaveBeenCalledWith(view, expect.any(Number));
  expect(bridge.observeSession).toHaveBeenCalledTimes(3);
  stop();
});

it('immediately uses the free observation slot after one response was lost', async () => {
  vi.useFakeTimers();
  const never = new Promise<SessionView>(() => {});
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    observeSession: vi.fn().mockReturnValueOnce(never).mockResolvedValue(view),
  };
  const stop = readLastSession(bridge, () => {});
  await vi.advanceTimersByTimeAsync(4001);
  expect(bridge.observeSession).toHaveBeenCalledTimes(2);
  requestSessionObservation(bridge);
  await vi.advanceTimersByTimeAsync(0);
  expect(bridge.observeSession).toHaveBeenCalledTimes(3);
  stop();
});

it('keeps looking for the last known snapshot after a temporary failure', async () => {
  vi.useFakeTimers();
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    observeSession: vi.fn().mockRejectedValueOnce(new Error('busy')).mockResolvedValue(view),
  };
  const listener = vi.fn();
  const stop = readLastSession(bridge, listener);
  await vi.advanceTimersByTimeAsync(2001);
  expect(listener).toHaveBeenCalledWith(view, expect.any(Number));
  stop();
});

it('does not mark normal snapshot recovery or old notices as a measurement gap', async () => {
  let notify: (state: SessionView['connection']) => void = () => {};
  const onGap = vi.fn();
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    enableConnectionEvents: async () => {},
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
      onGap={onGap}
    />,
  );
  await screen.findByText('При последней проверке связь с ядром подтверждена');
  act(() =>
    notify({
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Recovering,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  act(() =>
    notify({
      ...view.connection,
      revision: 0,
      status: ConnectionStateStatus.Disconnected,
      linkValid: false,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  expect(onGap).not.toHaveBeenCalled();
  act(() =>
    notify({
      ...view.connection,
      revision: 3,
      status: ConnectionStateStatus.Disconnected,
      linkValid: false,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  expect(onGap).toHaveBeenCalledTimes(1);
});

it('keeps the link visible during snapshot updates but revokes stale data and detects link loss', async () => {
  let notify: (state: SessionView['connection']) => void = () => {};
  const onView = vi.fn();
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({ ...view, validForMs: 6000, linkValidForMs: 6000 }),
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    enableConnectionEvents: async () => {},
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
      onView={onView}
    />,
  );
  await screen.findByText('При последней проверке связь с ядром подтверждена');
  act(() =>
    notify({
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Recovering,
      linkValid: true,
    }),
  );
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  expect(onView).toHaveBeenLastCalledWith(null, 0);
  act(() =>
    notify({
      ...view.connection,
      revision: 3,
      status: ConnectionStateStatus.Recovering,
      linkValid: false,
    }),
  );
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
});

it('explains a prolonged snapshot update without changing the link status', async () => {
  vi.useFakeTimers();
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({ ...view, validForMs: 6000, linkValidForMs: 6000 }),
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
      snapshotAccepted
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  mounted.rerender(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
      snapshotAccepted={false}
    />,
  );
  expect(screen.queryByText(/Ожидаем актуальное состояние ядра/)).toBeNull();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(500);
  });
  expect(screen.getByText(/Ожидаем актуальное состояние ядра/)).toBeTruthy();
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
});

it('refreshes immediately after live notification without sending another heartbeat', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  let latest = { ...view, validForMs: 6000 };
  const keepAlive = vi.fn(async () => {});
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive,
    sessionView: async () => latest,
    observeSession: async () => latest,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    enableConnectionEvents: async () => {},
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  act(() =>
    notify({
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Recovering,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  latest = { ...latest, connection: { ...latest.connection, revision: 3 } };
  await act(async () => {
    notify(latest.connection);
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  expect(keepAlive).toHaveBeenCalledTimes(1);
});

it('does not let an older observation restore freshness after heartbeat failure', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  let finish: (result: SessionView) => void = () => {};
  const delayed = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const keepAlive = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValue(new Error('lost'));
  const live = { ...view, validForMs: 6000 };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive,
    sessionView: async () => live,
    observeSession: vi.fn().mockResolvedValueOnce(live).mockReturnValue(delayed),
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    enableConnectionEvents: async () => {},
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  act(() =>
    notify({
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Live,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
  await act(async () => {
    finish({ ...live, connection: { ...live.connection, revision: 2 } });
    await delayed;
  });
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
});

it('marks a stream interruption before publishing the first recovered snapshot', async () => {
  vi.useFakeTimers();
  const received: string[] = [];
  let current = { ...view, validForMs: 6000 };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => current,
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
      onGap={() => received.push('gap')}
      onView={(state) => {
        if (state) received.push(`view:${state.connection.streamGeneration}`);
      }}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(received).toEqual(['view:0']);
  current = { ...current, connection: { ...current.connection, revision: 2, streamGeneration: 1 } };
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(received).toEqual(['view:0', 'gap', 'view:1']);
});

it('does not send a heartbeat when the last listener has gone before admission arrives', async () => {
  let finish: (ticket: string) => void = () => {};
  const pending = new Promise<string>((resolve) => {
    finish = resolve;
  });
  const bridge: SessionBridge = {
    beginSessionCheck: () => pending,
    keepAlive: vi.fn(() => Promise.resolve()),
    sessionView: vi.fn(() => Promise.resolve(view)),
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  mounted.unmount();
  await act(async () => {
    finish('ticket');
    await pending;
  });
  expect(bridge.keepAlive).not.toHaveBeenCalled();
});

it('recovers from one lost session response without unbounded calls', async () => {
  vi.useFakeTimers();
  const never = new Promise<void>(() => {});
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: vi.fn().mockReturnValueOnce(never).mockResolvedValue(undefined),
    sessionView: async () => view,
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(6500);
  });
  expect(bridge.keepAlive).toHaveBeenCalledTimes(2);
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  mounted.unmount();
});

it('invalidates the last confirmed view when a newer connection event arrives', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  const acknowledge = vi.fn(async () => {});
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    enableConnectionEvents: async () => {},
    acknowledgeConnection: acknowledge,
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  act(() =>
    notify({
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Recovering,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  expect(acknowledge).toHaveBeenCalledWith(view.connection.hostRunId, 2);
});

it('does not restore a view overtaken by a connection event', async () => {
  vi.useFakeTimers();
  let notify: (state: SessionView['connection']) => void = () => {};
  let finish: (result: SessionView) => void = () => {};
  const pending = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => pending,
    subscribeConnection: (callback) => {
      notify = callback;
      return () => {};
    },
    enableConnectionEvents: async () => {},
    acknowledgeConnection: async () => {},
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  act(() =>
    notify({
      ...view.connection,
      revision: 2,
      status: ConnectionStateStatus.Recovering,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    }),
  );
  await act(async () => {
    finish(view);
    await pending;
  });
  expect(screen.queryByText('При последней проверке связь с ядром подтверждена')).toBeNull();
});

it('detects a connection change even when the push notification is lost', async () => {
  vi.useFakeTimers();
  let finish: (state: ConnectionNotice) => void = () => {};
  const pending = new Promise<ConnectionNotice>((resolve) => {
    finish = resolve;
  });
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: () => () => {},
    enableConnectionEvents: async () => {},
    acknowledgeConnection: async () => {},
    waitConnection: vi
      .fn()
      .mockResolvedValueOnce({
        connection: view.connection,
        event: null,
        gap: false,
        snapshotBoundary: 0,
      })
      .mockReturnValueOnce(pending)
      .mockReturnValue(new Promise(() => {})),
  };
  render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  await act(async () => {
    finish({
      connection: {
        ...view.connection,
        revision: 2,
        status: ConnectionStateStatus.Recovering,
        linkValid: false,
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      event: null,
      gap: false,
      snapshotBoundary: 0,
    });
    await pending;
  });
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
});

it('recovers from one lost wait without accumulating unlimited calls', async () => {
  vi.useFakeTimers();
  const never = new Promise<ConnectionNotice>(() => {});
  const waitConnection = vi
    .fn()
    .mockReturnValueOnce(never)
    .mockResolvedValueOnce({
      connection: view.connection,
      event: null,
      gap: false,
      snapshotBoundary: 0,
    })
    .mockReturnValue(never);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => view,
    subscribeConnection: () => () => {},
    enableConnectionEvents: async () => {},
    acknowledgeConnection: async () => {},
    waitConnection,
  };
  const mounted = render(
    <SessionStatus
      bridge={bridge}
      hostRunId={view.connection.hostRunId}
      backendRunId={view.snapshot?.backendRunId ?? ''}
    />,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(6500);
  });
  expect(waitConnection).toHaveBeenCalledTimes(3);
  expect(screen.getByText('При последней проверке связь с ядром подтверждена')).toBeTruthy();
  mounted.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(20000);
  });
  expect(waitConnection).toHaveBeenCalledTimes(3);
});
