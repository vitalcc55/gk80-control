import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { beforeEach, expect, it, vi } from 'vitest';
import type { DesktopBridge, SessionBridge } from '../src/desktop/bridge';
import { DesktopApp } from '../src/desktop/DesktopApp';
import { LiveApp } from '../src/desktop/LiveApp';
import {
  CommandStatusStatus,
  ConnectionStateStatus,
  type DesktopStatus,
  DesktopStatusStatus,
  MeasurementChannel,
  MeasurementQuality,
  MeasurementQuantity,
  MeasurementSource,
  MeasurementUnit,
  ReadinessCheckSource,
  type SessionView,
  type Snapshot,
  TestParametersDirection,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { CreateCommandInput } from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/models';

const api = vi.hoisted(() => ({
  create: vi.fn(),
  readiness: vi.fn(),
  start: vi.fn(),
  confirm: vi.fn(),
  stop: vi.fn(),
  command: vi.fn(),
  list: vi.fn(),
  test: vi.fn(),
  measurements: vi.fn(),
  audit: vi.fn(),
}));
vi.mock('../src/components/MeasurementChart', () => ({
  MeasurementChart: () => <div role="img" aria-label="График показаний симулятора" />,
}));
vi.mock(
  '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/service',
  () => ({
    CreateTest: api.create,
    Readiness: api.readiness,
    StartTest: api.start,
    ConfirmAction: api.confirm,
    StopTest: api.stop,
    Command: api.command,
    ListTests: api.list,
    Test: api.test,
    Measurements: api.measurements,
    Audit: api.audit,
    BeginSessionCheck: vi.fn(),
    KeepAlive: vi.fn(),
    SessionView: vi.fn(),
    Snapshot: vi.fn(),
    ObserveSession: vi.fn(),
    EnableConnectionEvents: vi.fn(),
    AcknowledgeConnection: vi.fn(),
    AcknowledgeCoreEvent: vi.fn(),
    WaitConnection: vi.fn(),
  }),
);

const host = '11111111-1111-4111-8111-111111111111';
const backend = '22222222-2222-4222-8222-222222222222';
const testId = '33333333-3333-4333-8333-333333333333';
const actionId = '44444444-4444-4444-8444-444444444444';
const stopId = '55555555-5555-4555-8555-555555555555';
const record = {
  testId,
  stopCommandId: stopId,
  createdAt: '2026-10-30T00:00:00Z',
  operatorId: 'demo',
  demo: true,
  stage: TestRecordStage.Preparing,
  outcome: TestRecordOutcome.TestRecordOutcomePending,
  dataQuality: TestRecordDataQuality.TestRecordDataQualityPending,
  saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
  parameters: {
    valve: 'К-1',
    areaM2: 1,
    pmaxPa: 1000,
    measurementSeconds: 10,
    direction: TestParametersDirection.TestParametersDirectionPressure,
    actuator: false,
  },
};

beforeEach(() => {
  sessionStorage.clear();
  vi.resetAllMocks();
});

it('shows diagnostics already degraded when the live session opens', async () => {
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: { apiVersion: 'v1', backendRunId: backend, demo: true },
    diagnosticsDegraded: true,
  };
  const desktop: DesktopBridge = {
    status: async () => ready,
    verify: async () => ready,
    subscribe: () => () => {},
  };
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 0,
        lastEventSequence: 0,
        activeTest: null,
        pendingAction: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  render(<DesktopApp bridge={desktop} session={session} />);
  expect(await screen.findByText('Журнал диагностики недоступен или неполон.')).toBeTruthy();
  expect(await screen.findByRole('button', { name: 'Новое испытание' })).toBeTruthy();
});

it('preserves the live form when diagnostics degrade', async () => {
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: { apiVersion: 'v1', backendRunId: backend, demo: true },
    diagnosticsDegraded: false,
  };
  let notify: (status: DesktopStatus) => void = () => {};
  const desktop: DesktopBridge = {
    status: async () => ready,
    verify: async () => ready,
    subscribe: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 0,
        lastEventSequence: 0,
        activeTest: null,
        pendingAction: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  render(<DesktopApp bridge={desktop} session={session} />);
  const user = userEvent.setup();
  const create = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(create.hasAttribute('disabled')).toBe(false));
  await user.click(create);
  const input = screen.getByLabelText('Обозначение или наименование клапана');
  await user.type(input, 'Клапан');
  act(() => notify({ ...ready, revision: 2, diagnosticsDegraded: true }));
  expect(await screen.findByText('Журнал диагностики недоступен или неполон.')).toBeTruthy();
  expect(screen.getByLabelText('Обозначение или наименование клапана')).toBe(input);
  expect((input as HTMLInputElement).value).toBe('Клапан');
});

it('keeps stop available when diagnostics degrade during a live test', async () => {
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: { apiVersion: 'v1', backendRunId: backend, demo: true },
    diagnosticsDegraded: false,
  };
  let notify: (status: DesktopStatus) => void = () => {};
  const desktop: DesktopBridge = {
    status: async () => ready,
    verify: async () => ready,
    subscribe: (callback) => {
      notify = callback;
      return () => {};
    },
  };
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 2,
        lastEventSequence: 2,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  });
  render(<DesktopApp bridge={desktop} session={session} />);
  const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
  act(() => notify({ ...ready, revision: 2, diagnosticsDegraded: true }));
  expect(await screen.findByText('Журнал диагностики недоступен или неполон.')).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Остановить испытание' })).toBe(stop);
  await userEvent.setup().click(stop);
  expect(api.stop).toHaveBeenCalledOnce();
  expect(api.stop).toHaveBeenCalledWith(testId, { commandId: stopId });
  expect(api.create).not.toHaveBeenCalled();
  expect(api.start).not.toHaveBeenCalled();
});

it('uses core confirmation and measurements for the operator path', async () => {
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  };
  let lastCommand = {
    commandId: '',
    status: CommandStatusStatus.CommandStatusStatusUnknown,
    testId,
  };
  const commands = new Map<string, typeof lastCommand>();
  let activeRecord = record;
  let startId = '';
  let hostRevision = 0;
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async (): Promise<SessionView> => ({
      connection: {
        hostRunId: host,
        revision: ++hostRevision,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockImplementation(async (id: string) => commands.get(id));
  api.create.mockImplementation(async (input: CreateCommandInput) => {
    lastCommand = {
      commandId: input.commandId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
      testId,
    };
    commands.set(input.commandId, lastCommand);
    activeRecord = { ...record, parameters: input.parameters };
    snapshot = { ...snapshot, activeTest: activeRecord, revision: 1, lastEventSequence: 1 };
    return { delivery: 'response', command: lastCommand };
  });
  api.readiness.mockResolvedValue({
    testId,
    revision: 1,
    ready: true,
    checks: [
      {
        code: 'demo',
        label: 'Симулятор',
        source: ReadinessCheckSource.ReadinessCheckSourceSimulator,
        ready: true,
      },
    ],
  });
  api.start.mockImplementation(async (_id: string, input: { commandId: string }) => {
    startId = input.commandId;
    lastCommand = {
      commandId: input.commandId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    };
    commands.set(input.commandId, lastCommand);
    snapshot = {
      ...snapshot,
      activeTest: { ...activeRecord, stage: TestRecordStage.Running },
      revision: 2,
      lastEventSequence: 2,
      pendingAction: { testId, actionId, instruction: 'Подтвердите измерение' },
    };
    return new Promise(() => {});
  });
  api.confirm.mockImplementation(
    async (_id: string, _action: string, input: { commandId: string }) => {
      lastCommand = {
        commandId: input.commandId,
        status: CommandStatusStatus.CommandStatusStatusCompleted,
        testId,
      };
      commands.set(input.commandId, lastCommand);
      commands.set(startId, { ...lastCommand, commandId: startId });
      snapshot = {
        ...snapshot,
        lastEventSequence: 3,
        pendingAction: null,
        measurements: [
          {
            channel: MeasurementChannel.Dd1,
            measuredAt: '2026-10-30T00:00:01Z',
            observedAt: '2026-10-30T00:00:01Z',
            quality: MeasurementQuality.MeasurementQualityValid,
            quantity: MeasurementQuantity.MeasurementQuantityPressure,
            sequence: 1,
            source: MeasurementSource.MeasurementSourceSimulator,
            unit: MeasurementUnit.Pa,
            value: 1000,
          },
        ],
      };
      return { delivery: 'response', command: lastCommand };
    },
  );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(api.create).toHaveBeenCalledTimes(1);
  expect(api.create).toHaveBeenCalledWith(
    expect.objectContaining({
      parameters: expect.objectContaining({ areaM2: 0.012, pmaxPa: 10000, measurementSeconds: 60 }),
    }),
  );
  await screen.findByText('Проверки демонстрационного ядра', {}, { timeout: 8000 });
  await user.click(screen.getByRole('button', { name: 'К параметрам' }));
  expect(screen.getByText('Текущее испытание · только просмотр')).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'Вернуться к испытанию' }));
  await user.click(screen.getByLabelText(/Клапан закрыт/));
  await user.click(screen.getByLabelText(/Подключение соответствует направлению/));
  await user.click(screen.getByRole('button', { name: /Перейти к проведению/ }));
  expect(api.start).toHaveBeenCalledTimes(1);
  const confirm = await screen.findByRole(
    'button',
    { name: 'Подтвердить действие' },
    { timeout: 8000 },
  );
  await waitFor(() => expect(confirm.hasAttribute('disabled')).toBe(false));
  await user.click(confirm);
  expect(api.confirm).toHaveBeenCalledTimes(1);
  await screen.findByText(/≈ 1\s?000 Па/, {}, { timeout: 8000 });
  snapshot = {
    ...snapshot,
    activeTest: {
      ...activeRecord,
      stage: TestRecordStage.Finished,
      outcome: TestRecordOutcome.TestRecordOutcomeCompleted,
    },
    lastEventSequence: 4,
  };
  await screen.findByRole('heading', { name: 'Результат испытания' }, { timeout: 8000 });
  expect(screen.queryByRole('heading', { name: 'Параметры испытания' })).toBeNull();
}, 18000);

it('does not send another creation after an unknown response and remount', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.create.mockRejectedValue(new Error('response lost'));
  api.command.mockRejectedValue(new Error('not confirmed'));
  const mounted = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  await screen.findByText(/Исход команды пока неизвестен/);
  mounted.unmount();
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const restored = await screen.findByRole('button', { name: 'Новое испытание' });
  expect(restored.hasAttribute('disabled')).toBe(true);
  expect(api.create).toHaveBeenCalledTimes(1);
  expect(screen.getByText(/Исход команды:/)).toBeTruthy();
}, 10000);

it('allows a new attempt only when the host confirms the first was not sent', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  let finish: (value: { delivery: string; command: null }) => void = () => {};
  const reply = new Promise<{ delivery: string; command: null }>((resolve) => {
    finish = resolve;
  });
  api.create.mockReturnValue(reply);
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    status: CommandStatusStatus.CommandStatusStatusUnknown,
  }));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  await screen.findByText(/Исход команды: unknown/);
  await act(async () => {
    finish({ delivery: 'notSent', command: null });
    await reply;
  });
  await screen.findByText(/Команда не отправлена/);
  expect(
    screen.getByRole('button', { name: /Проверить готовность/ }).hasAttribute('disabled'),
  ).toBe(false);
  expect(api.create).toHaveBeenCalledTimes(1);
}, 10000);

it('allows stopping while another command is unresolved and reports a confirmed failure', async () => {
  const startId = '66666666-6666-4666-8666-666666666666';
  sessionStorage.setItem(
    'gk80.demo.pending-command',
    JSON.stringify([{ id: startId, kind: 'start', testId, retired: false }]),
  );
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: { testId, actionId, instruction: 'Подтвердите измерение' },
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockResolvedValue({
    commandId: startId,
    status: CommandStatusStatus.CommandStatusStatusUnknown,
    testId,
  });
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusFailed,
    testId,
    reason: 'simulator_stop_failed',
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
  await user.click(stop);
  expect(api.stop).toHaveBeenCalledTimes(1);
  expect(api.stop).toHaveBeenCalledWith(testId, { commandId: stopId });
  await screen.findByText(/Остановка не выполнена или отклонена/);
});

it('offers the reserved stop after remount even when heartbeat cannot be confirmed', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {
      throw new Error('unavailable');
    },
    observeSession: async () => ({
      connection: {
        hostRunId: host,
        revision: 2,
        status: ConnectionStateStatus.Recovering,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 0,
      linkValidForMs: 0,
    }),
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 2,
        status: ConnectionStateStatus.Recovering,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 0,
      linkValidForMs: 0,
    }),
  };
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
  await userEvent.setup().click(stop);
  expect(api.stop).toHaveBeenCalledWith(testId, { commandId: stopId });
  expect(api.start).not.toHaveBeenCalled();
});

it('continues checking Stop after the interrupted result and keeps saving separate', async () => {
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurements: [],
  };
  let revision = 0;
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: ++revision,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  });
  api.command.mockImplementation(async () => ({
    commandId: stopId,
    status:
      snapshot.activeTest?.stage === 'finished'
        ? CommandStatusStatus.CommandStatusStatusCompleted
        : CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  }));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  snapshot = {
    ...snapshot,
    revision: 3,
    lastEventSequence: 3,
    activeTest: {
      ...record,
      stage: TestRecordStage.Finished,
      outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
      saveStatus: TestRecordSaveStatus.TestRecordSaveStatusFailed,
    },
  };
  await screen.findByText('Остановка подтверждена ядром.', {}, { timeout: 8000 });
  expect(screen.getByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  expect(screen.getByText(/ошибка сохранения/i)).toBeTruthy();
}, 12000);

it('does not let an older confirmed view hide a newer active test from fallback', async () => {
  const older: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  };
  const newer: Snapshot = { ...older, revision: 1, lastEventSequence: 1, activeTest: record };
  let finish: (value: SessionView) => void = () => {};
  const pending = new Promise<SessionView>((resolve) => {
    finish = resolve;
  });
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => pending,
    observeSession: async () => ({
      connection: {
        hostRunId: host,
        revision: 2,
        status: ConnectionStateStatus.Recovering,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot: newer,
      validForMs: 0,
      linkValidForMs: 0,
    }),
  };
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await screen.findByRole('button', { name: 'Остановить испытание' });
  await act(async () => {
    finish({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot: older,
      validForMs: 6000,
      linkValidForMs: 6000,
    });
    await pending;
  });
  expect(screen.getByRole('button', { name: 'Остановить испытание' })).toBeTruthy();
  expect(screen.getByText('Состояние управления не подтверждено')).toBeTruthy();
  expect(api.start).not.toHaveBeenCalled();
});

it('keeps an earlier unknown Stop observable when stopping a later test', async () => {
  const oldStop = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
  const nextTest = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
  const nextStop = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc';
  sessionStorage.setItem(
    'gk80.demo.pending-command',
    JSON.stringify([{ id: oldStop, kind: 'stop', testId, retired: true }]),
  );
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 5,
    lastEventSequence: 5,
    activeTest: {
      ...record,
      testId: nextTest,
      stopCommandId: nextStop,
      stage: TestRecordStage.Running,
    },
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 5,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    status: CommandStatusStatus.CommandStatusStatusUnknown,
  }));
  api.stop.mockReturnValue(new Promise(() => {}));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  const saved: { id: string }[] = JSON.parse(
    sessionStorage.getItem('gk80.demo.pending-command') ?? '[]',
  );
  expect(saved.map((item) => item.id)).toEqual(expect.arrayContaining([oldStop, nextStop]));
  expect(api.stop).toHaveBeenCalledWith(nextTest, { commandId: nextStop });
});

it('keeps Stop available and reserves its identifier when the command list is full', async () => {
  const old = Array.from({ length: 64 }, (_, index) => ({
    id: `old-${index}`,
    kind: 'create',
    testId: null,
    retired: true,
  }));
  sessionStorage.setItem('gk80.demo.pending-command', JSON.stringify(old));
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockReturnValue(new Promise(() => {}));
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  const saved: { id: string }[] = JSON.parse(
    sessionStorage.getItem('gk80.demo.pending-command') ?? '[]',
  );
  expect(saved).toHaveLength(65);
  expect(saved.some((item) => item.id === stopId)).toBe(true);
  expect(api.stop).toHaveBeenCalledTimes(1);
});

it('keeps an unpersisted Stop observable across remount when session storage fails', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    status: CommandStatusStatus.CommandStatusStatusUnknown,
  }));
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  });
  const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  const mounted = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  expect(api.stop).toHaveBeenCalledTimes(1);
  expect(screen.getByText(/отслеживается только в памяти/)).toBeTruthy();
  mounted.unmount();
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  expect(await screen.findByText(new RegExp(stopId))).toBeTruthy();
  setItem.mockRestore();
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    status: CommandStatusStatus.CommandStatusStatusRejected,
  }));
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить состояние' }));
  await waitFor(() => expect(screen.queryByText(/отслеживается только в памяти/)).toBeNull());
});

it('merges the previously unread command list before storing an emergency Stop', async () => {
  const oldId = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd';
  sessionStorage.setItem(
    'gk80.demo.pending-command',
    JSON.stringify([{ id: oldId, kind: 'create', testId: null, retired: false }]),
  );
  const originalGet = Storage.prototype.getItem;
  const getItem = vi
    .spyOn(Storage.prototype, 'getItem')
    .mockImplementationOnce(() => {
      throw new Error('unavailable');
    })
    .mockImplementation(function (this: Storage, key: string) {
      return originalGet.call(this, key);
    });
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockReturnValue(new Promise(() => {}));
  api.stop.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
    testId,
  });
  render(
    <StrictMode>
      <LiveApp session={bridge} hostRunId={host} backendRunId={backend} />
    </StrictMode>,
  );
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  getItem.mockRestore();
  const saved: { id: string }[] = JSON.parse(
    sessionStorage.getItem('gk80.demo.pending-command') ?? '[]',
  );
  expect(saved.map((item) => item.id)).toEqual(expect.arrayContaining([oldId, stopId]));
  expect(api.stop).toHaveBeenCalledTimes(1);
});

it('can retry reading an unavailable command list without sending a command', async () => {
  const oldId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee';
  sessionStorage.setItem(
    'gk80.demo.pending-command',
    JSON.stringify([{ id: oldId, kind: 'start', testId, retired: false }]),
  );
  const originalGet = Storage.prototype.getItem;
  const getItem = vi
    .spyOn(Storage.prototype, 'getItem')
    .mockImplementationOnce(() => {
      throw new Error('unavailable');
    })
    .mockImplementation(function (this: Storage, key: string) {
      return originalGet.call(this, key);
    });
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    activeTest: null,
    pendingAction: null,
    measurements: [],
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockReturnValue(new Promise(() => {}));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
  getItem.mockRestore();
  expect(screen.getByText(/Исход команды:/)).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
    true,
  );
  expect(api.create).not.toHaveBeenCalled();
});
