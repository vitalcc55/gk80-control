import { act, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { SessionBridge } from '../src/desktop/bridge';
import { SessionStatus } from '../src/desktop/SessionStatus';
import {
  ConnectionStateStatus,
  type SessionView,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

afterEach(() => vi.useRealTimers());

const view: SessionView = {
  connection: {
    hostRunId: '8d43fc4c-7bf7-408f-a8e8-19d0dfd725c4',
    revision: 1,
    status: ConnectionStateStatus.Live,
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
};

function call<T>(promise: Promise<T>): Promise<T> {
  return promise;
}

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

it('shares an outstanding check across remounts without queuing more calls', async () => {
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
  expect(bridge.keepAlive).toHaveBeenCalledTimes(1);
  expect(bridge.sessionView).not.toHaveBeenCalled();
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
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
