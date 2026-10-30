import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { expect, it, vi } from 'vitest';
import type { DesktopBridge } from '../src/desktop/bridge';
import { DesktopApp } from '../src/desktop/DesktopApp';
import {
  type DesktopStatus,
  DesktopStatusStatus,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

function status(revision: number, state: DesktopStatusStatus): DesktopStatus {
  return {
    hostRunId: '8d43fc4c-7bf7-408f-a8e8-19d0dfd725c4',
    revision,
    connectionRevision: revision,
    status: state,
    handshake:
      state === DesktopStatusStatus.DesktopStatusStatusReady
        ? { apiVersion: 'v1', backendRunId: '6c229f38-3426-4608-bd72-dcd0d3b2bcd7', demo: true }
        : null,
    diagnosticsDegraded: false,
  };
}

it('does not let an old verification reply restore a core that exited', async () => {
  let notify: (value: DesktopStatus) => void = () => {};
  let finish: (value: DesktopStatus) => void = () => {};
  const reply = new Promise<DesktopStatus>((resolve) => {
    finish = resolve;
  });
  const unsubscribe = vi.fn();
  const bridge: DesktopBridge = {
    status: async () => status(1, DesktopStatusStatus.DesktopStatusStatusReady),
    verify: () => reply,
    subscribe: (callback) => {
      notify = callback;
      return unsubscribe;
    },
  };
  const view = render(
    <StrictMode>
      <DesktopApp bridge={bridge} />
    </StrictMode>,
  );
  expect(await screen.findByText('При запуске ядро ответило')).toBeTruthy();
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить связь' }));
  expect(
    screen.getByRole('button', { name: 'Проверяем…' }).getAttribute('disabled'),
  ).not.toBeNull();
  act(() => notify(status(3, DesktopStatusStatus.DesktopStatusStatusExited)));
  await act(async () => {
    finish(status(2, DesktopStatusStatus.DesktopStatusStatusReady));
    await reply;
  });
  expect(screen.getByText('Связь с ядром потеряна')).toBeTruthy();
  expect(screen.queryByText(/Ядро ответило/)).toBeNull();
  view.unmount();
  expect(unsubscribe).toHaveBeenCalled();
});

it('shows an occupied profile and degraded diagnostics separately', async () => {
  const degraded = {
    ...status(2, DesktopStatusStatus.DesktopStatusStatusProfileInUse),
    diagnosticsDegraded: true,
  };
  const bridge: DesktopBridge = {
    status: async () => degraded,
    verify: async () => degraded,
    subscribe: () => () => {},
  };
  render(<DesktopApp bridge={bridge} />);
  expect(await screen.findByText('Профиль уже открыт')).toBeTruthy();
  expect(screen.getByText('Журнал диагностики недоступен или неполон.')).toBeTruthy();
  expect(screen.getByText('Демонстрация · тестовые данные')).toBeTruthy();
});

it('ignores a delayed RPC failure after a newer event', async () => {
  let notify: (value: DesktopStatus) => void = () => {};
  let rejectStatus: () => void = () => {};
  const initial = new Promise<DesktopStatus>((_resolve, reject) => {
    rejectStatus = () => reject(new Error('unavailable'));
  });
  const bridge: DesktopBridge = {
    status: () => initial,
    verify: async () => status(2, DesktopStatusStatus.DesktopStatusStatusReady),
    subscribe: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  render(<DesktopApp bridge={bridge} />);
  act(() => notify(status(2, DesktopStatusStatus.DesktopStatusStatusReady)));
  await act(async () => {
    rejectStatus();
    await initial.catch(() => {});
  });
  expect(screen.getByText('При запуске ядро ответило')).toBeTruthy();
  expect(screen.queryByText('Не удалось получить состояние приложения')).toBeNull();
});

it('keeps a bridge error on stale events and permits an explicit retry', async () => {
  let notify: (value: DesktopStatus) => void = () => {};
  const ready = status(1, DesktopStatusStatus.DesktopStatusStatusReady);
  const verify = vi
    .fn<DesktopBridge['verify']>()
    .mockRejectedValueOnce(new Error('unavailable'))
    .mockResolvedValueOnce({ ...ready, revision: 2, diagnosticsDegraded: true });
  const bridge: DesktopBridge = {
    status: async () => ready,
    verify,
    subscribe: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  render(<DesktopApp bridge={bridge} />);
  await screen.findByText('При запуске ядро ответило');
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'Проверить связь' }));
  expect(await screen.findByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByText(/Ядро ответило/)).toBeNull();
  act(() => notify(ready));
  expect(screen.getByText('Не удалось получить состояние приложения')).toBeTruthy();
  act(() => notify({ ...ready, revision: 2, diagnosticsDegraded: true }));
  expect(screen.getByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByText(/Ядро ответило/)).toBeNull();
  expect(screen.getByText('Журнал диагностики недоступен или неполон.')).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'Повторить связь' }));
  expect(await screen.findByText('При запуске ядро ответило')).toBeTruthy();
});

it('never presents ready without a confirmed demo handshake', async () => {
  const invalid = { ...status(1, DesktopStatusStatus.DesktopStatusStatusReady), handshake: null };
  const bridge: DesktopBridge = {
    status: async () => invalid,
    verify: async () => invalid,
    subscribe: () => () => {},
  };
  render(<DesktopApp bridge={bridge} />);
  expect(await screen.findByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByText('При запуске ядро ответило')).toBeNull();
});

it('does not hide a failed check behind a diagnostics-only update', async () => {
  let notify: (value: DesktopStatus) => void = () => {};
  let rejectVerify: () => void = () => {};
  const pending = new Promise<DesktopStatus>((_resolve, reject) => {
    rejectVerify = () => reject(new Error('unavailable'));
  });
  const ready = status(1, DesktopStatusStatus.DesktopStatusStatusReady);
  const bridge: DesktopBridge = {
    status: async () => ready,
    verify: () => pending,
    subscribe: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  render(<DesktopApp bridge={bridge} />);
  await screen.findByText('При запуске ядро ответило');
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить связь' }));
  act(() => notify({ ...ready, revision: 2, diagnosticsDegraded: true }));
  await act(async () => {
    rejectVerify();
    await pending.catch(() => {});
  });
  expect(screen.getByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByText(/Ядро ответило/)).toBeNull();
  expect(screen.getByText('Журнал диагностики недоступен или неполон.')).toBeTruthy();
});

it('does not let an older status request hide a later failed check', async () => {
  let notify: (value: DesktopStatus) => void = () => {};
  let resolveStatus: (value: DesktopStatus) => void = () => {};
  const pendingStatus = new Promise<DesktopStatus>((resolve) => {
    resolveStatus = resolve;
  });
  const ready = status(1, DesktopStatusStatus.DesktopStatusStatusReady);
  const bridge: DesktopBridge = {
    status: () => pendingStatus,
    verify: async () => {
      throw new Error('unavailable');
    },
    subscribe: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  render(<DesktopApp bridge={bridge} />);
  act(() => notify(ready));
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить связь' }));
  expect(await screen.findByText('Не удалось получить состояние приложения')).toBeTruthy();
  await act(async () => {
    resolveStatus({ ...ready, revision: 2, diagnosticsDegraded: true });
    await pendingStatus;
  });
  expect(screen.getByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByText(/Ядро ответило/)).toBeNull();
});

it('rejects a nil backend run identifier', async () => {
  const ready = status(1, DesktopStatusStatus.DesktopStatusStatusReady);
  if (!ready.handshake) throw new Error('test handshake missing');
  ready.handshake.backendRunId = '00000000-0000-0000-0000-000000000000';
  const bridge: DesktopBridge = {
    status: async () => ready,
    verify: async () => ready,
    subscribe: () => () => {},
  };
  render(<DesktopApp bridge={bridge} />);
  expect(await screen.findByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByText('При запуске ядро ответило')).toBeNull();
});
