import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { type ComponentProps, Profiler, StrictMode } from 'react';
import { beforeEach, expect, it, vi } from 'vitest';
import type { DesktopBridge, SessionBridge } from '../src/desktop/bridge';
import { createBrowserBridge } from '../src/desktop/browser-bridge';
import { DesktopApp as RealDesktopApp } from '../src/desktop/DesktopApp';
import { CommandConflictError } from '../src/desktop/demo-service';
import { History } from '../src/desktop/History';
import { LiveApp as RealLiveApp } from '../src/desktop/LiveApp';
import { MeasurementSeriesContext } from '../src/desktop/measurement-series';
import { sessionObservation } from '../src/desktop/session-observation';
import { desktopDemoService } from '../src/desktop/wails-bridge';
import {
  type CommandDispatch,
  CommandDispatchDelivery,
  type CommandStatus,
  CommandStatusStatus,
  ConnectionStateStatus,
  type DesktopStatus,
  DesktopStatusStatus,
  MeasurementChannel,
  MeasurementQuality,
  MeasurementQuantity,
  MeasurementSource,
  MeasurementUnit,
  type Readiness,
  ReadinessCheckSource,
  type SessionView,
  type Snapshot,
  type TestList,
  TestParametersDirection,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { CreateCommandInput } from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/models';
import type { GraphPoint } from '../src/model';

const api = vi.hoisted(() => ({
  create: vi.fn(),
  readiness: vi.fn(),
  start: vi.fn(),
  confirm: vi.fn(),
  stop: vi.fn(),
  command: vi.fn(),
  exact: vi.fn(),
  list: vi.fn(),
  test: vi.fn(),
  measurements: vi.fn(),
  audit: vi.fn(),
}));
function DesktopApp(props: ComponentProps<typeof RealDesktopApp>) {
  return <RealDesktopApp {...props} service={props.service ?? desktopDemoService} />;
}

function LiveApp(
  props: Omit<ComponentProps<typeof RealLiveApp>, 'service' | 'databaseId'> & {
    service?: ComponentProps<typeof RealLiveApp>['service'];
    databaseId?: string;
  },
) {
  return (
    <RealLiveApp
      {...props}
      databaseId={props.databaseId ?? database}
      service={props.service ?? desktopDemoService}
    />
  );
}
vi.mock('../src/components/MeasurementChart', () => ({
  MeasurementChart: ({ points }: { points: readonly GraphPoint[] }) => (
    <div role="img" aria-label="График показаний симулятора" data-points={JSON.stringify(points)} />
  ),
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
    LookupCommandExact: api.exact,
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

const stopResponse = <T,>(command: T) => ({ delivery: 'response', command });

const host = '11111111-1111-4111-8111-111111111111';
const backend = '22222222-2222-4222-8222-222222222222';
const database = '99999999-9999-4999-8999-999999999999';
const pendingKey = `gk80.demo.pending-command.v1:${database}`;
const testId = '33333333-3333-4333-8333-333333333333';
const actionId = '44444444-4444-4444-8444-444444444444';
const stopId = '55555555-5555-4555-8555-555555555555';
function savedAttempt(
  id: string,
  kind: 'create' | 'start' | 'confirm' | 'stop',
  savedTestId: string | null,
  retired: boolean,
) {
  const action = kind === 'confirm' ? actionId : null;
  const body =
    kind === 'create'
      ? {
          commandId: id,
          expectedRevision: 0,
          parameters: {
            valve: 'V',
            areaM2: 1,
            pmaxPa: 1000,
            measurementSeconds: 10,
            direction: 'pressure',
            actuator: false,
          },
        }
      : kind === 'start'
        ? { commandId: id, expectedRevision: 0, valveClosed: true, connectionConfirmed: true }
        : kind === 'confirm'
          ? { commandId: id, expectedRevision: 0 }
          : { commandId: id };
  return {
    schema: 2,
    id,
    kind,
    testId: savedTestId,
    actionId: action,
    originalJSON: JSON.stringify(body),
    retired,
  };
}
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

it('isolates a series-only update from hidden screens and keeps the completed result reachable', async () => {
  const measurementContext = new MeasurementSeriesContext();
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 1,
    lastEventSequence: 1,
    measurementBatch: 1,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: snapshot.revision,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  const commits = vi.fn();
  render(
    <Profiler id="operator-view" onRender={commits}>
      <LiveApp
        session={session}
        hostRunId={host}
        backendRunId={backend}
        measurementContext={measurementContext}
      />
    </Profiler>,
  );
  await screen.findByRole('heading', { name: 'Проведение испытания' });
  await userEvent.setup().click(screen.getByRole('button', { name: 'Испытания' }));
  await screen.findByText('История пуста');
  commits.mockClear();
  const key = `${host}:${backend}:${database}:${testId}`;
  const next = {
    ...snapshot,
    measurementBatch: 2,
    measurements:
      snapshot.measurements?.map((item) => ({
        ...item,
        sequence: 2,
        measuredAt: '2026-10-30T00:00:02Z',
        value: 1001,
      })) ?? [],
  };
  act(() => measurementContext.accept(key, next, true, 0));
  expect(measurementContext.read(key).points.at(-1)?.pressure).toBe(1001);
  expect(commits).not.toHaveBeenCalled();
  snapshot = {
    ...snapshot,
    revision: 2,
    lastEventSequence: 2,
    activeTest: {
      ...record,
      stage: TestRecordStage.Finished,
      outcome: TestRecordOutcome.TestRecordOutcomeCompleted,
    },
  };
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Результат прохода' }, { timeout: 8000 }));
  expect(screen.getByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  expect(screen.getByText('Завершено')).toBeTruthy();
  expect(api.stop).not.toHaveBeenCalled();
}, 12000);

it.each(['history', 'bridge'] as const)(
  'retains chart points and time origin through %s without sending commands',
  async (route) => {
    const sample = (index: number): Snapshot => ({
      apiVersion: 'v1',
      backendRunId: backend,
      revision: index,
      lastEventSequence: index,
      measurementBatch: index,
      activeTest: { ...record, stage: TestRecordStage.Running },
      pendingAction: null,
      measurementTiming: null,
      measurements: [
        {
          channel: MeasurementChannel.Dd1,
          measuredAt: `2026-10-30T00:00:0${index}Z`,
          observedAt: `2026-10-30T00:00:0${index}Z`,
          quality: MeasurementQuality.MeasurementQualityValid,
          quantity: MeasurementQuantity.MeasurementQuantityPressure,
          sequence: index,
          source: MeasurementSource.MeasurementSourceSimulator,
          unit: MeasurementUnit.Pa,
          value: 999 + index,
        },
      ],
    });
    let snapshot = sample(1);
    let revision = 0;
    const session: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
        connection: {
          hostRunId: host,
          revision: ++revision,
          status: ConnectionStateStatus.Live,
          linkValid: true,
          leaseAcquireUnknown: false,
          streamGeneration: 0,
        },
        snapshot,
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    const desktop: DesktopBridge = {
      status: async () => ({
        hostRunId: host,
        revision: 1,
        connectionRevision: 1,
        status: DesktopStatusStatus.DesktopStatusStatusReady,
        diagnosticsDegraded: false,
        handshake: { apiVersion: 'v1', backendRunId: backend, databaseId: database, demo: true },
      }),
      verify: async () => desktop.status(),
      subscribe: () => () => {},
    };
    const measurementContext = new MeasurementSeriesContext();
    const view = render(
      <DesktopApp bridge={desktop} session={session} measurementContext={measurementContext} />,
    );
    const points = () =>
      screen.getByRole('img', { name: 'График показаний симулятора' }).getAttribute('data-points');
    await waitFor(() => expect(points()).toContain('1000'));
    const currentNavigation = screen.getByRole('button', { name: 'Текущее испытание' });
    expect(currentNavigation.getAttribute('aria-current')).toBe('page');
    currentNavigation.focus();
    snapshot = sample(2);
    await waitFor(() => expect(points()).toContain('1001'), { timeout: 8000 });
    expect(document.activeElement).toBe(currentNavigation);
    const user = userEvent.setup();
    if (route === 'history') {
      await user.click(screen.getByRole('button', { name: 'Испытания' }));
      expect(document.activeElement).toBe(screen.getByRole('main', { name: 'Испытания' }));
      expect(screen.getByRole('button', { name: 'Испытания' }).getAttribute('aria-current')).toBe(
        'page',
      );
      expect(screen.getByRole('button', { name: 'Остановить испытание' })).toBeTruthy();
    } else {
      view.rerender(
        <DesktopApp
          bridge={{
            ...desktop,
            status: async () => {
              throw new Error('unavailable');
            },
          }}
          session={session}
          measurementContext={measurementContext}
        />,
      );
      await screen.findByRole('button', { name: 'Повторить связь' });
    }
    expect(screen.queryByRole('img', { name: 'График показаний симулятора' })).toBeNull();
    snapshot = sample(route === 'history' ? 4 : 3);
    if (route === 'history') {
      await waitFor(
        () =>
          expect(
            measurementContext
              .read(`${host}:${backend}:${database}:${testId}`)
              .points.map((point) => point.pressure),
          ).toEqual([1000, 1001, null, 1003]),
        { timeout: 8000 },
      );
      expect(screen.queryByRole('img', { name: 'График показаний симулятора' })).toBeNull();
      snapshot = sample(5);
      await waitFor(
        () =>
          expect(
            measurementContext
              .read(`${host}:${backend}:${database}:${testId}`)
              .points.map((point) => point.pressure),
          ).toEqual([1000, 1001, null, 1003, 1004]),
        { timeout: 8000 },
      );
      expect(screen.queryByRole('img', { name: 'График показаний симулятора' })).toBeNull();
      await user.click(screen.getByRole('button', { name: 'Текущее испытание' }));
    } else
      view.rerender(
        <DesktopApp bridge={desktop} session={session} measurementContext={measurementContext} />,
      );
    await waitFor(
      () =>
        expect(points()).toBe(
          JSON.stringify([
            { seconds: 0, pressure: 1000, differential: null },
            { seconds: 1, pressure: 1001, differential: null },
            ...(route === 'bridge'
              ? [
                  { seconds: 1.5, pressure: null, differential: null },
                  { seconds: 2, pressure: 1002, differential: null },
                ]
              : [
                  { seconds: 2, pressure: null, differential: null },
                  { seconds: 3, pressure: 1003, differential: null },
                  { seconds: 4, pressure: 1004, differential: null },
                ]),
          ]),
        ),
      { timeout: 8000 },
    );
    for (const command of [api.create, api.start, api.confirm, api.stop])
      expect(command).not.toHaveBeenCalled();
  },
  30000,
);

beforeEach(() => {
  sessionStorage.clear();
  vi.resetAllMocks();
  api.exact.mockImplementation(
    async (_database: string, _kind: string, target: string, _action: string, id: string) => {
      const command: CommandStatus = await api.command(id);
      return command.status === 'unknown'
        ? { outcome: 'unknown', command: null }
        : {
            outcome: 'match',
            command: { ...command, testId: command.testId ?? (target || testId) },
          };
    },
  );
});

it('does not attribute an unbound legacy command to the confirmed database', async () => {
  const legacy = JSON.stringify([
    { id: 'legacy', kind: 'create', testId: null, retired: false },
    { id: 'legacy-stop', kind: 'stop', testId, retired: false },
  ]);
  sessionStorage.setItem('gk80.demo.pending-command', legacy);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  render(
    <LiveApp session={bridge} hostRunId={host} backendRunId={backend} databaseId={database} />,
  );
  await screen.findByText(/прежние команды с неизвестной принадлежностью/i);
  expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
    true,
  );
  expect(api.command).not.toHaveBeenCalledWith('legacy');
  expect(api.create).not.toHaveBeenCalled();
  expect(sessionStorage.getItem('gk80.demo.pending-command')).toBe(legacy);
  expect(screen.getByText(/Прежняя команда: создание испытания, ID legacy/)).toBeTruthy();
  expect(
    screen.getByText(/Прежняя команда: остановка испытания, ID legacy-stop/).textContent,
  ).toContain('Безопасное давление не подтверждено');
  await userEvent.setup().click(screen.getByRole('button', { name: 'Я проверил прежние команды' }));
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
      false,
    ),
  );
  expect(api.command).not.toHaveBeenCalledWith('legacy');
  expect(sessionStorage.getItem('gk80.demo.pending-command')).toBe(legacy);
});

it.each([false, true])(
  'keeps a restored command without its original content unknown (receiptRequired=%s)',
  async (receiptRequired) => {
    const id = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
    sessionStorage.setItem(
      pendingKey,
      JSON.stringify([{ id, kind: 'create', testId: null, retired: false, receiptRequired }]),
    );
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 0,
          lastEventSequence: 0,
          measurementBatch: null,
          activeTest: null,
          pendingAction: null,
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    api.command.mockResolvedValue({
      commandId: id,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    });
    render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    await screen.findByText(new RegExp(`исход неизвестен.*${id}`));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить состояние' }));
    expect(api.command).not.toHaveBeenCalledWith(id);
    expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
      true,
    );
    await userEvent.setup().click(screen.getByRole('button', { name: 'Я проверил эту команду' }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
        false,
      ),
    );
    expect(screen.getByText(new RegExp(`исход неизвестен.*${id}`))).toBeTruthy();
  },
);

it('restores the browser profile pending attempt from shared storage without repeating it', async () => {
  const browserDatabase = '77777777-7777-4777-8777-777777777777';
  const id = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const attempt = savedAttempt(id, 'create', null, false);
  localStorage.setItem(key, JSON.stringify([attempt]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  try {
    const mounted = render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={browserDatabase}
        pendingStorage={localStorage}
      />,
    );
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        browserDatabase,
        'create',
        '',
        '',
        id,
        attempt.originalJSON,
      ),
    );
    mounted.unmount();
    const newer = savedAttempt('17171717-1717-4717-8717-171717171717', 'create', null, false);
    localStorage.setItem(key, JSON.stringify([newer]));
    render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={browserDatabase}
        pendingStorage={localStorage}
      />,
    );
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        browserDatabase,
        'create',
        '',
        '',
        newer.id,
        newer.originalJSON,
      ),
    );
    expect(api.create).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(key)).toBeNull();
  } finally {
    localStorage.removeItem(key);
  }
});

it('keeps Stop available when the legacy command list cannot be read', async () => {
  const originalGet = Storage.prototype.getItem;
  const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (
    this: Storage,
    key: string,
  ) {
    if (key === 'gk80.demo.pending-command') throw new Error('legacy list unavailable');
    return originalGet.call(this, key);
  });
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockResolvedValue({
    commandId: stopId,
    status: CommandStatusStatus.CommandStatusStatusUnknown,
  });
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
    }),
  );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await screen.findByText(/Не удалось прочитать прежний список команд/i);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  expect(api.stop).toHaveBeenCalledOnce();
  expect(api.create).not.toHaveBeenCalled();
  getItem.mockRestore();
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Повторить чтение прежних команд' }));
  expect(screen.queryByText(/Не удалось прочитать прежний список команд/i)).toBeNull();
});

it('isolates pending commands and late lookups across databases', async () => {
  const otherDatabase = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
  const commandId = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
  const item = JSON.stringify([savedAttempt(commandId, 'create', null, false)]);
  const otherKey = `gk80.demo.pending-command.v1:${otherDatabase}`;
  sessionStorage.setItem(pendingKey, item);
  sessionStorage.setItem(otherKey, item);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finishOld: (value: CommandStatus) => void = () => {};
  const old = new Promise<CommandStatus>((resolve) => {
    finishOld = resolve;
  });
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockReturnValueOnce(old).mockResolvedValue({
    commandId,
    status: CommandStatusStatus.CommandStatusStatusAccepted,
  });
  const view = render(
    <LiveApp session={bridge} hostRunId={host} backendRunId={backend} databaseId={database} />,
  );
  await waitFor(() => expect(api.command).toHaveBeenCalledTimes(1));
  view.rerender(
    <LiveApp session={bridge} hostRunId={host} backendRunId={backend} databaseId={otherDatabase} />,
  );
  await waitFor(() => expect(api.command.mock.calls.length).toBeGreaterThanOrEqual(2));
  await screen.findByText(/принята; завершение команды не подтверждено/);
  await act(async () => {
    finishOld({ commandId, status: CommandStatusStatus.CommandStatusStatusFailed });
    await old;
  });
  expect(screen.getByText(/принята; завершение команды не подтверждено/)).toBeTruthy();
  expect(sessionStorage.getItem(pendingKey)).toBe(item);
  expect(JSON.parse(sessionStorage.getItem(otherKey) ?? '[]')).toEqual([
    expect.objectContaining(savedAttempt(commandId, 'create', null, false)),
  ]);
  expect(api.create).not.toHaveBeenCalled();
});

it('tracks Stop separately from a restored ordinary command with the same identifier', async () => {
  const oldTest = '77777777-7777-4777-8777-777777777777';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([
      { id: stopId, kind: 'start', testId: oldTest, retired: true, receiptRequired: false },
    ]),
  );
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockResolvedValue({
    commandId: stopId,
    testId,
    status: CommandStatusStatus.CommandStatusStatusUnknown,
  });
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    }),
  );
  const mounted = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
  await userEvent.setup().click(stop);
  await screen.findByText('Остановка подтверждена ядром.');
  expect(api.stop).toHaveBeenCalledOnce();
  expect(api.stop).toHaveBeenCalledWith(testId, { commandId: stopId });
  const rows = screen.getAllByText(/Команда:/);
  expect(
    rows.some(
      (row) =>
        row.textContent?.includes('исход неизвестен') &&
        !row.textContent.includes('Остановка испытания'),
    ),
  ).toBe(true);
  expect(
    rows.some(
      (row) =>
        row.textContent?.includes('выполнена') && row.textContent.includes('Остановка испытания'),
    ),
  ).toBe(true);
  const stored = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
  expect(stored).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        id: stopId,
        kind: 'start',
        testId: oldTest,
        receiptRequired: true,
      }),
      expect.objectContaining({ id: stopId, kind: 'stop', testId }),
    ]),
  );
  mounted.unmount();
  snapshot = {
    ...snapshot,
    revision: 3,
    lastEventSequence: 3,
    measurementBatch: null,
    activeTest: {
      ...record,
      testId: '88888888-8888-4888-8888-888888888888',
      stopCommandId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
      stage: TestRecordStage.Running,
    },
  };
  api.command.mockResolvedValue({
    commandId: stopId,
    testId,
    status: CommandStatusStatus.CommandStatusStatusCompleted,
  });
  api.command.mockClear();
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const stopCheck = (await screen.findAllByRole('button', { name: 'Проверить состояние' }))[1];
  if (!stopCheck) throw new Error('historical Stop check missing');
  await userEvent.setup().click(stopCheck);
  await waitFor(() => expect(api.command).toHaveBeenCalledWith(stopId));
  await waitFor(() =>
    expect(
      screen
        .getAllByText(/Команда:/)
        .some(
          (row) =>
            row.textContent?.includes('выполнена') &&
            row.textContent.includes('Остановка испытания'),
        ),
    ).toBe(true),
  );
  expect(
    screen
      .getAllByText(/Команда:/)
      .some(
        (row) =>
          row.textContent?.includes('исход неизвестен') &&
          !row.textContent.includes('Остановка испытания'),
      ),
  ).toBe(true);
}, 10000);

it('shows diagnostics already degraded when the live session opens', async () => {
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: {
      apiVersion: 'v1',
      backendRunId: backend,
      databaseId: '9a0dce72-7219-45fc-8025-568b49e70919',
      demo: true,
    },
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
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
    handshake: {
      apiVersion: 'v1',
      backendRunId: backend,
      databaseId: '9a0dce72-7219-45fc-8025-568b49e70919',
      demo: true,
    },
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
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

it('retains the browser form when a replacement bridge cannot confirm status', async () => {
  const heartbeat = vi.fn(async () => {});
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: { apiVersion: 'v1', backendRunId: backend, databaseId: database, demo: true },
    diagnosticsDegraded: false,
  };
  const desktop: DesktopBridge = {
    status: async () => ready,
    verify: async () => ready,
    subscribe: () => () => {},
  };
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: heartbeat,
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  const view = render(<DesktopApp bridge={desktop} session={session} retainLiveOnError />);
  const user = userEvent.setup();
  const create = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(create.hasAttribute('disabled')).toBe(false));
  await user.click(create);
  const input = screen.getByLabelText('Обозначение или наименование клапана');
  await user.type(input, 'Клапан');
  const disconnected: DesktopBridge = {
    ...desktop,
    status: async () => {
      throw new Error('disconnected');
    },
  };
  view.rerender(<DesktopApp bridge={disconnected} session={session} retainLiveOnError />);
  expect(await screen.findByText('Не удалось подтвердить состояние приложения.')).toBeTruthy();
  expect(screen.queryByLabelText('Обозначение или наименование клапана')).toBeNull();
  const sent = heartbeat.mock.calls.length;
  await new Promise((resolve) => setTimeout(resolve, 2200));
  expect(heartbeat).toHaveBeenCalledTimes(sent);
  view.rerender(<DesktopApp bridge={desktop} session={session} retainLiveOnError />);
  const restored = await screen.findByLabelText('Обозначение или наименование клапана');
  expect((restored as HTMLInputElement).value).toBe('Клапан');
});

it('does not open a browser live screen for an unconfirmed demo handshake', async () => {
  const invalid: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: { apiVersion: 'v1', backendRunId: backend, databaseId: database, demo: false },
    diagnosticsDegraded: false,
  };
  const desktop: DesktopBridge = {
    status: async () => invalid,
    verify: async () => invalid,
    subscribe: () => () => {},
  };
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => {
      throw new Error('unexpected');
    },
  };
  render(<DesktopApp bridge={desktop} session={session} retainLiveOnError />);
  expect(await screen.findByText('Не удалось получить состояние приложения')).toBeTruthy();
  expect(screen.queryByRole('button', { name: 'Новое испытание' })).toBeNull();
});

it('accepts a verified browser bridge when the initial status read fails', async () => {
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: { apiVersion: 'v1', backendRunId: backend, databaseId: database, demo: true },
    diagnosticsDegraded: false,
  };
  const desktop: DesktopBridge = {
    status: async () => {
      throw new Error('unavailable');
    },
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  render(<DesktopApp bridge={desktop} session={session} retainLiveOnError />);
  await userEvent.setup().click(await screen.findByRole('button', { name: 'Повторить связь' }));
  expect(await screen.findByRole('button', { name: 'Новое испытание' })).toBeTruthy();
});

it('keeps stop available when diagnostics degrade during a live test', async () => {
  const ready: DesktopStatus = {
    hostRunId: host,
    revision: 1,
    connectionRevision: 1,
    status: DesktopStatusStatus.DesktopStatusStatusReady,
    handshake: {
      apiVersion: 'v1',
      backendRunId: backend,
      databaseId: '9a0dce72-7219-45fc-8025-568b49e70919',
      demo: true,
    },
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    }),
  );
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

it('finishes a pending history list and uses the replacement for later reads', async () => {
  let finish: (value: TestList) => void = () => {};
  const late = new Promise<TestList>((resolve) => {
    finish = resolve;
  });
  const listTests = vi.fn().mockReturnValue(late);
  const service = { ...desktopDemoService, listTests };
  const nextListTests = vi.fn().mockResolvedValue({ items: [], nextAfter: null });
  const nextService = { ...desktopDemoService, listTests: nextListTests };
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const view = render(
    <LiveApp session={session} service={service} hostRunId={host} backendRunId={backend} />,
  );
  await waitFor(() => expect(listTests).toHaveBeenCalledOnce());
  view.rerender(
    <LiveApp session={session} service={nextService} hostRunId={host} backendRunId={backend} />,
  );
  await act(async () => {
    finish({ items: [record], nextAfter: null });
    await late;
  });
  expect(await screen.findByRole('button', { name: /К-1/ })).toBeTruthy();
  expect(nextListTests).not.toHaveBeenCalled();
  await userEvent.setup().click(screen.getByRole('button', { name: 'К началу списка' }));
  await waitFor(() => expect(nextListTests).toHaveBeenCalledOnce());
  expect(api.list).not.toHaveBeenCalled();
});

it('keeps a requested history page when the same session replaces its service wrapper', async () => {
  let finish: (value: TestList) => void = () => {};
  const response = new Promise<TestList>((resolve) => {
    finish = resolve;
  });
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [record], nextAfter: testId })
    .mockReturnValueOnce(response);
  const nextListTests = vi.fn();
  const service = { ...desktopDemoService, listTests };
  const replacement = { ...desktopDemoService, listTests: nextListTests };
  const callbacks = { onNew: vi.fn(), onCopy: vi.fn(), onDiscard: vi.fn() };
  const view = render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: 'Следующая страница' }));
  view.rerender(
    <History
      {...callbacks}
      service={replacement}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  await act(async () => {
    finish({
      items: [{ ...record, parameters: { ...record.parameters, valve: 'К-2' } }],
      nextAfter: null,
    });
    await response;
  });
  expect(await screen.findByRole('button', { name: /К-2/ })).toBeTruthy();
  expect(listTests).toHaveBeenCalledWith({ limit: 20, after: testId });
  expect(nextListTests).not.toHaveBeenCalled();
});

it('finishes opening history when the same session replaces its service wrapper', async () => {
  let finish: (value: typeof record) => void = () => {};
  const response = new Promise<typeof record>((resolve) => {
    finish = resolve;
  });
  api.list.mockResolvedValue({ items: [record], nextAfter: null });
  api.test.mockReturnValue(response);
  const oldMeasurements = vi.fn().mockRejectedValue(new Error('old transport unavailable'));
  const oldAudit = vi.fn().mockRejectedValue(new Error('old transport unavailable'));
  const measurements = vi.fn().mockResolvedValue({ items: [], nextAfter: null });
  const audit = vi.fn().mockResolvedValue({ items: [], nextAfter: null });
  const service = { ...desktopDemoService, measurements: oldMeasurements, audit: oldAudit };
  const replacement = { ...desktopDemoService, measurements, audit };
  const callbacks = { onNew: vi.fn(), onCopy: vi.fn(), onDiscard: vi.fn() };
  const view = render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /К-1/ }));
  view.rerender(
    <History
      {...callbacks}
      service={replacement}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  await act(async () => {
    finish(record);
    await response;
  });
  expect(await screen.findByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  expect(measurements).not.toHaveBeenCalled();
  expect(audit).not.toHaveBeenCalled();
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(screen.getByText('Аудит', { selector: 'summary' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledOnce());
  await waitFor(() => expect(audit).toHaveBeenCalledOnce());
  expect(oldMeasurements).not.toHaveBeenCalled();
  expect(oldAudit).not.toHaveBeenCalled();
});

it('explains a canceled start in the saved audit', async () => {
  api.list.mockResolvedValue({ items: [record], nextAfter: null });
  api.test.mockResolvedValue(record);
  api.measurements.mockResolvedValue({ items: [], nextAfter: null });
  api.audit.mockResolvedValue({
    items: [
      {
        sequence: 1,
        occurredAt: '2026-10-30T00:00:00Z',
        action: 'start',
        outcome: 'failed',
        operatorId: 'system',
        reason: 'canceled_by_stop',
      },
    ],
    nextAfter: null,
  });
  render(
    <History
      service={desktopDemoService}
      available
      disabled={false}
      draftAvailable={false}
      onNew={vi.fn()}
      onCopy={vi.fn()}
      onDiscard={vi.fn()}
    />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /К-1/ }));
  await userEvent.setup().click(await screen.findByText('Аудит', { selector: 'summary' }));
  expect(
    await screen.findByText(/причина запуск отменён при остановке до подтверждения оператора/),
  ).toBeTruthy();
});

it('does not reuse a pending readiness response after the core identity changes', async () => {
  let finish: (value: Readiness) => void = () => {};
  const delayed = new Promise<Readiness>((resolve) => {
    finish = resolve;
  });
  const readiness = vi
    .fn()
    .mockReturnValueOnce(delayed)
    .mockResolvedValue({
      testId,
      revision: 1,
      ready: false,
      checks: [
        {
          code: 'demo',
          label: 'Новая проверка',
          source: ReadinessCheckSource.ReadinessCheckSourceSimulator,
          ready: false,
        },
      ],
    });
  const service = { ...desktopDemoService, readiness };
  const makeSession = (hostRunId: string, backendRunId: string): SessionBridge => ({
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId,
        revision: 1,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: record,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  });
  const replacementHost = '77777777-7777-4777-8777-777777777777';
  const replacementBackend = '88888888-8888-4888-8888-888888888888';
  const first = makeSession(host, backend);
  const second = makeSession(replacementHost, replacementBackend);
  const view = render(
    <LiveApp session={first} service={service} hostRunId={host} backendRunId={backend} />,
  );
  await waitFor(() => expect(readiness).toHaveBeenCalledTimes(1));
  view.rerender(
    <LiveApp
      session={second}
      service={service}
      hostRunId={replacementHost}
      backendRunId={replacementBackend}
    />,
  );
  expect(await screen.findByText('Новая проверка')).toBeTruthy();
  expect(readiness).toHaveBeenCalledTimes(2);
  await act(async () => {
    finish({
      testId,
      revision: 1,
      ready: true,
      checks: [
        {
          code: 'demo',
          label: 'Старая проверка',
          source: ReadinessCheckSource.ReadinessCheckSourceSimulator,
          ready: true,
        },
      ],
    });
    await delayed;
  });
  expect(screen.queryByText('Старая проверка')).toBeNull();
});

it('preserves the unsaved form when the service wrapper changes', async () => {
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  const service = { ...desktopDemoService };
  const nextService = { ...desktopDemoService };
  const view = render(
    <LiveApp session={session} service={service} hostRunId={host} backendRunId={backend} />,
  );
  const user = userEvent.setup();
  const create = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(create.hasAttribute('disabled')).toBe(false));
  await user.click(create);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'Клапан');
  view.rerender(
    <LiveApp session={session} service={nextService} hostRunId={host} backendRunId={backend} />,
  );
  const input = await screen.findByLabelText('Обозначение или наименование клапана');
  expect((input as HTMLInputElement).value).toBe('Клапан');
});

it('uses core confirmation and measurements for the operator path', async () => {
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
    expect(JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]')).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: input.commandId,
          kind: 'create',
          schema: 2,
          actionId: null,
          originalJSON: JSON.stringify(input),
        }),
      ]),
    );
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
    expect(JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]')).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: input.commandId,
          kind: 'start',
          testId,
          schema: 2,
          actionId: null,
          originalJSON: JSON.stringify(input),
        }),
      ]),
    );
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
      expect(JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]')).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            id: input.commandId,
            kind: 'confirm',
            testId,
            schema: 2,
            actionId,
            originalJSON: JSON.stringify(input),
          }),
        ]),
      );
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
        measurementTiming: null,
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
  await screen.findByRole('heading', { name: 'Автоматические проверки' }, { timeout: 8000 });
  expect(screen.queryByRole('button', { name: 'Вернуться к испытанию' })).toBeNull();
  expect(screen.getByRole('button', { name: 'Остановить испытание' })).toBeTruthy();
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
  await screen.findByText(/Команда: принята; завершение команды не подтверждено/);
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

it('does not infer a legacy Stop without its original contents', async () => {
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([{ id: stopId, kind: 'stop', testId, retired: false }]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  expect(await screen.findByText(/Исходное содержимое команды не сохранилось/)).toBeTruthy();
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить состояние' }));
  expect(api.exact).not.toHaveBeenCalled();
  expect(api.command).not.toHaveBeenCalled();
  expect(api.stop).not.toHaveBeenCalled();
});

it('persists the Stop attempt before sending and reconciles it after remount without resending', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const body = JSON.stringify({ commandId: stopId });
  api.stop.mockImplementation(async () => {
    const attempts = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
    expect(attempts).toContainEqual(
      expect.objectContaining({ kind: 'stop', id: stopId, testId, originalJSON: body }),
    );
    throw new Error('response lost');
  });
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  const mounted = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText(/Исход остановки неизвестен/);
  mounted.unmount();
  api.exact.mockResolvedValue({
    outcome: 'match',
    command: { commandId: stopId, testId, status: 'completed' },
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await waitFor(() =>
    expect(api.exact).toHaveBeenCalledWith(database, 'stop', testId, '', stopId, body),
  );
  await screen.findByText(/Команда: выполнена/);
  expect(api.stop).toHaveBeenCalledOnce();
});

it('distinguishes a Stop lookup conflict from rejecting the actual request', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({ outcome: 'conflict', command: null });
  api.stop.mockResolvedValue(stopResponse({ commandId: stopId, testId, status: 'accepted' }));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  expect(await screen.findByText(/Сохранённая запись принадлежит другой команде/)).toBeTruthy();
  expect(screen.queryByText(/Запрос остановки отклонён/)).toBeNull();
  expect(screen.getByText(new RegExp(`Остановка испытания ${testId}`))).toBeTruthy();
  expect(api.stop).toHaveBeenCalledOnce();
});

it('keeps a storage conflict visible after the direct Stop succeeds', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finish: (value: { commandId: string; testId: string; status: string }) => void = () => {};
  const response = new Promise<{ commandId: string; testId: string; status: string }>((resolve) => {
    finish = resolve;
  });
  api.exact.mockResolvedValue({ outcome: 'conflict', command: null });
  api.stop.mockReturnValue(response.then(stopResponse));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText(/Сохранённая запись принадлежит другой команде/);
  await act(async () => {
    finish({ commandId: stopId, testId, status: 'completed' });
    await response;
  });
  expect(await screen.findByText('Остановка подтверждена ядром.')).toBeTruthy();
  expect(screen.getByText(/Сохранённая запись принадлежит другой команде/)).toBeTruthy();
});

it('retains a confirmed Stop in volatile memory after remount', async () => {
  const volatileDatabase = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.stop.mockResolvedValue(stopResponse({ commandId: stopId, testId, status: 'completed' }));
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  try {
    const mounted = render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={volatileDatabase}
      />,
    );
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    await screen.findByText('Остановка подтверждена ядром.');
    mounted.unmount();
    const restored = render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={volatileDatabase}
      />,
    );
    expect(await screen.findByText(/Команда: выполнена/)).toBeTruthy();
    api.stop.mockRejectedValue(new Error('response lost'));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Остановить испытание' }));
    expect(await screen.findByText('Остановка подтверждена ядром.')).toBeTruthy();
    expect(api.stop).toHaveBeenCalledTimes(2);
    setItem.mockRestore();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    restored.unmount();
    const persisted = render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={volatileDatabase}
      />,
    );
    expect(await screen.findByText(/Команда: выполнена/)).toBeTruthy();
    persisted.unmount();
    const originalGet = Storage.prototype.getItem;
    const unavailable = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (
      this: Storage,
      key,
    ) {
      if (key === `gk80.demo.pending-command.v1:${volatileDatabase}`)
        throw new Error('unavailable');
      return originalGet.call(this, key);
    });
    try {
      render(
        <LiveApp
          session={bridge}
          hostRunId={host}
          backendRunId={backend}
          databaseId={volatileDatabase}
        />,
      );
      await userEvent
        .setup()
        .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
      expect(await screen.findByText('Остановка подтверждена ядром.')).toBeTruthy();
      expect(screen.getByText(/Команда: выполнена/)).toBeTruthy();
      expect(api.stop).toHaveBeenCalledTimes(3);
    } finally {
      unavailable.mockRestore();
    }
  } finally {
    setItem.mockRestore();
  }
});

it('does not repeatedly write a confirmed observation while storage remains unavailable', async () => {
  const browserDatabase = '80808080-8080-4080-8080-808080808080';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const attempt = savedAttempt('90909090-9090-4090-8090-909090909090', 'create', null, false);
  localStorage.setItem(key, JSON.stringify([attempt]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({
    outcome: 'match',
    command: { commandId: attempt.id, testId, status: 'completed' },
  });
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  try {
    render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={browserDatabase}
        pendingStorage={localStorage}
      />,
    );
    await screen.findByRole('button', { name: 'Повторить чтение команд' });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(failure).toHaveBeenCalledTimes(1);
    expect(screen.getByText(new RegExp(`ID ${attempt.id}`))).toBeTruthy();
    failure.mockRestore();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    await waitFor(() => expect(screen.queryByText(new RegExp(`ID ${attempt.id}`))).toBeNull());
    expect(api.create).not.toHaveBeenCalled();
  } finally {
    failure.mockRestore();
    localStorage.removeItem(key);
  }
});

it('does not give an old Stop without contents the result of a new attempt', async () => {
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([{ id: stopId, kind: 'stop', testId, retired: false }]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.stop.mockResolvedValue(stopResponse({ commandId: stopId, testId, status: 'completed' }));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  const old = screen.getByText(/Исходное содержимое команды не сохранилось/);
  expect(old.textContent).toContain('исход неизвестен');
  expect(old.textContent).not.toContain('Команда: выполнена');
  expect(screen.getByText('Остановка подтверждена ядром.')).toBeTruthy();
});

it('does not attribute another Stop with the same ID but different JSON to the current request', async () => {
  const browserDatabase = '70707070-7070-4070-8070-707070707070';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const old = savedAttempt(stopId, 'stop', testId, false);
  old.originalJSON += ' ';
  localStorage.setItem(key, JSON.stringify([old]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finish: (value: { outcome: string; command: CommandStatus }) => void = () => {};
  const lookup = new Promise<{ outcome: string; command: CommandStatus }>((resolve) => {
    finish = resolve;
  });
  api.exact.mockImplementation(async (_database, _kind, _test, _action, _id, originalJSON) =>
    originalJSON === old.originalJSON ? lookup : { outcome: 'unknown', command: null },
  );
  let finishStop: (value: CommandStatus) => void = () => {};
  const stop = new Promise<CommandStatus>((resolve) => {
    finishStop = resolve;
  });
  api.stop.mockReturnValue(stop.then(stopResponse));
  try {
    render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={browserDatabase}
        pendingStorage={localStorage}
      />,
    );
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        browserDatabase,
        'stop',
        testId,
        '',
        stopId,
        old.originalJSON,
      ),
    );
    await userEvent.setup().click(screen.getByRole('button', { name: 'Остановить испытание' }));
    await act(async () => {
      finish({
        outcome: 'match',
        command: {
          commandId: stopId,
          testId,
          status: CommandStatusStatus.CommandStatusStatusCompleted,
        },
      });
      await lookup;
    });
    expect(await screen.findByText(/Команда: выполнена/)).toBeTruthy();
    expect(screen.queryByText('Остановка подтверждена ядром.')).toBeNull();
    expect(screen.getByText(/Запрошена остановка/)).toBeTruthy();
    await act(async () => {
      finishStop({
        commandId: stopId,
        testId,
        status: CommandStatusStatus.CommandStatusStatusAccepted,
      });
      await stop;
    });
  } finally {
    localStorage.removeItem(key);
  }
});

it.each([
  ['preparing', true, 'none'],
  ['finished', false, 'none'],
  ['finished', true, 'reopen'],
  ['finished', true, 'edit'],
  ['preparing', true, 'edit'],
] as const)(
  'preserves the draft when Create is reconciled after a first %s snapshot',
  async (firstStage, editDraft, retainMode) => {
    const retainBefore = retainMode !== 'none';
    let snapshot: Snapshot = {
      apiVersion: 'v1',
      backendRunId: backend,
      revision: 0,
      lastEventSequence: 0,
      measurementBatch: null,
      activeTest: null,
      pendingAction: null,
      measurementTiming: null,
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
          leaseAcquireUnknown: false,
          streamGeneration: 0,
        },
        snapshot,
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    let finishLookup: (status: CommandStatus) => void = () => {};
    const lookup = new Promise<CommandStatus>((resolve) => {
      finishLookup = resolve;
    });
    api.command.mockReturnValue(lookup);
    api.create.mockImplementation(async () => {
      if (!retainBefore)
        snapshot = {
          ...snapshot,
          activeTest:
            firstStage === 'preparing'
              ? record
              : {
                  ...record,
                  stage: TestRecordStage.Finished,
                  outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
                },
          revision: 1,
          lastEventSequence: 1,
        };
      throw new Error('response lost');
    });
    api.readiness.mockResolvedValue({
      testId,
      revision: 1,
      ready: true,
      checks: [],
    });
    render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    const user = userEvent.setup();
    const create = await screen.findByRole('button', { name: 'Новое испытание' });
    await waitFor(() => expect(create.hasAttribute('disabled')).toBe(false));
    await user.click(create);
    await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
    await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
    await user.type(screen.getByLabelText(/производителя, Па/), '10000');
    await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
    if (retainBefore) {
      await screen.findByText(/Исход команды пока неизвестен/);
      if (retainMode === 'reopen') {
        await user.click(screen.getByRole('button', { name: 'Испытания' }));
        await user.click(screen.getByRole('button', { name: 'Параметры' }));
      }
      await user.type(screen.getByLabelText('Обозначение или наименование клапана'), ' новый');
      snapshot = {
        ...snapshot,
        activeTest:
          firstStage === 'preparing'
            ? record
            : {
                ...record,
                stage: TestRecordStage.Finished,
                outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
              },
        revision: 1,
        lastEventSequence: 1,
      };
      if (firstStage === 'finished') {
        await screen.findByRole('button', { name: 'Результат прохода' }, { timeout: 7000 });
        expect(screen.getByLabelText('Обозначение или наименование клапана')).toHaveProperty(
          'value',
          'К-1 новый',
        );
      }
    }
    if (firstStage === 'preparing') {
      await screen.findByRole('heading', { name: /готовност/i }, { timeout: 7000 });
      expect(screen.getByRole('main').getAttribute('aria-label')).toMatch(/Готовност/i);
      snapshot = {
        ...snapshot,
        activeTest: {
          ...record,
          stage: TestRecordStage.Finished,
          outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
        },
        revision: 2,
        lastEventSequence: 2,
      };
    }
    if (!retainBefore || firstStage === 'preparing') {
      expect(
        await screen.findByRole('heading', { name: 'Результат испытания' }, { timeout: 7000 }),
      ).toBeTruthy();
      await user.click(screen.getByRole('button', { name: 'Параметры' }));
    }
    const draftField = screen.getByLabelText('Обозначение или наименование клапана');
    if (editDraft && !retainBefore) await user.type(draftField, ' новый');
    const createCall = api.create.mock.calls[0];
    if (!createCall) throw new Error('Create was not sent');
    await act(async () => {
      finishLookup({
        commandId: createCall[0].commandId,
        testId,
        status: CommandStatusStatus.CommandStatusStatusCompleted,
      });
      await lookup;
    });
    await waitFor(() => expect(screen.queryByText(/Команда: исход неизвестен/)).toBeNull());
    expect(screen.getByLabelText('Обозначение или наименование клапана')).toHaveProperty(
      'value',
      editDraft ? 'К-1 новый' : 'К-1',
    );
  },
  15000,
);

it('does not send another creation after an unknown response and remount', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
  expect(screen.getByText(/Команда:/)).toBeTruthy();
}, 10000);

it.each(['wails', 'browser'])(
  'does not attribute a previous command to a conflicting %s creation',
  async (adapter) => {
    const snapshot: Snapshot = {
      apiVersion: 'v1',
      backendRunId: backend,
      revision: 0,
      lastEventSequence: 0,
      measurementBatch: null,
      activeTest: null,
      pendingAction: null,
      measurementTiming: null,
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
          leaseAcquireUnknown: false,
          streamGeneration: 0,
        },
        snapshot,
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    let resolveCreate: (value: CommandDispatch) => void = () => {};
    const response = new Promise<CommandDispatch>((resolve) => {
      resolveCreate = resolve;
    });
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    api.create.mockReturnValue(response);
    api.command.mockImplementation(async (id: string) => ({
      commandId: id,
      testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    }));
    api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
    const fetcher = vi.fn<typeof fetch>(async () => Response.json(await response));
    const browser = createBrowserBridge(
      { generation: 'test', signal: new AbortController().signal },
      fetcher,
    ).service;
    const service =
      adapter === 'browser'
        ? { ...desktopDemoService, createTest: browser.createTest }
        : desktopDemoService;
    render(<LiveApp session={bridge} service={service} hostRunId={host} backendRunId={backend} />);
    const user = userEvent.setup();
    const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
    await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
    await user.click(newTest);
    const valve = screen.getByLabelText('Обозначение или наименование клапана') as HTMLInputElement;
    await user.type(valve, 'К-1');
    await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
    await user.type(screen.getByLabelText(/производителя, Па/), '10000');
    await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
    await screen.findByText(/Команда: исход неизвестен/);
    await user.click(screen.getByRole('button', { name: 'Проверить состояние' }));
    expect(screen.queryByText(/Команда: выполнена/)).toBeNull();
    expect(valve.value).toBe('К-1');
    await act(async () => {
      resolveCreate({
        delivery: CommandDispatchDelivery.CommandDispatchDeliveryConflict,
        command: null,
      });
      await response;
    });
    await screen.findByText(/идентификатор.*другой команде/i);
    expect(screen.queryByText(/Команда: выполнена/)).toBeNull();
    expect(valve.value).toBe('К-1');
    if (adapter === 'browser') expect(fetcher).toHaveBeenCalledOnce();
    else expect(api.create).toHaveBeenCalledOnce();
  },
  10000,
);

it.each(['lost', 'malformed'] as const)(
  'keeps a browser creation unknown after a %s response and service replacement',
  async (responseKind) => {
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 0,
          lastEventSequence: 0,
          measurementBatch: null,
          activeTest: null,
          pendingAction: null,
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    if (responseKind === 'lost') api.create.mockRejectedValue(new Error('response lost'));
    else
      api.create.mockImplementation(async (command: CreateCommandInput) => ({
        delivery: 'response',
        command: { commandId: command.commandId, testId },
      }));
    api.command.mockImplementation(async (id: string) => ({
      commandId: id,
      testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    }));
    api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
    const fetcher = vi.fn<typeof fetch>(async (_path, options) => {
      const command: unknown = JSON.parse(String(options?.body));
      return Response.json(await api.create(command));
    });
    const browser = {
      ...desktopDemoService,
      createTest: createBrowserBridge(
        { generation: 'test', signal: new AbortController().signal },
        fetcher,
      ).service.createTest,
    };
    const mounted = render(
      <LiveApp session={bridge} service={browser} hostRunId={host} backendRunId={backend} />,
    );
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
    render(
      <LiveApp
        session={bridge}
        service={desktopDemoService}
        hostRunId={host}
        backendRunId={backend}
      />,
    );
    await screen.findByText(/Команда: исход неизвестен/);
    await user.click(screen.getByRole('button', { name: 'Проверить состояние' }));
    expect(screen.queryByText(/Команда: выполнена/)).toBeNull();
    expect(api.command).not.toHaveBeenCalled();
    expect(api.create).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
    const sent = api.create.mock.calls[0]?.[0];
    if (!sent) throw new Error('create was not sent');
    expect(api.exact).toHaveBeenCalledWith(
      database,
      'create',
      '',
      '',
      sent.commandId,
      JSON.stringify(sent),
    );
    const commandId: string = sent.commandId;
    api.exact.mockResolvedValue({
      outcome: 'match',
      command: { commandId, testId, status: 'completed' },
    });
    await user.click(screen.getByRole('button', { name: 'Проверить состояние' }));
    await screen.findByText(/Команда: выполнена/);
    expect(api.create).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
  },
  10000,
);

it.each([
  'conflict',
  'unknown',
  'databaseMismatch',
  'wrongId',
  'badTestId',
  'badReason',
  'badEnvelope',
])('keeps exact lookup outcome %s separate from another command', async (outcome) => {
  const id = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
  const attempt = savedAttempt(id, 'create', null, false);
  sessionStorage.setItem(pendingKey, JSON.stringify([attempt]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.exact.mockResolvedValue(
    outcome === 'wrongId'
      ? { outcome: 'match', command: { commandId: 'other', testId, status: 'completed' } }
      : outcome === 'badTestId'
        ? { outcome: 'match', command: { commandId: id, testId: {}, status: 'completed' } }
        : outcome === 'badReason'
          ? {
              outcome: 'match',
              command: { commandId: id, testId, status: 'completed', reason: {} },
            }
          : outcome === 'badEnvelope'
            ? { outcome: 'conflict', command: null, extra: true }
            : { outcome, command: null },
  );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await waitFor(() =>
    expect(api.exact).toHaveBeenCalledWith(database, 'create', '', '', id, attempt.originalJSON),
  );
  if (outcome === 'conflict') await screen.findByText(/Идентификатор принадлежит другой команде/);
  else await screen.findByText(/Команда: исход неизвестен/);
  expect(screen.queryByText(/Команда: выполнена/)).toBeNull();
  expect(api.command).not.toHaveBeenCalledWith(id);
  expect(api.create).not.toHaveBeenCalled();
});

it('does not replace a confirmed command with a late unknown exact lookup', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finishCreate: (value: CommandDispatch) => void = () => {};
  let finishLookup: (value: { outcome: string; command: null }) => void = () => {};
  const createResponse = new Promise<CommandDispatch>((resolve) => {
    finishCreate = resolve;
  });
  const lookupResponse = new Promise<{ outcome: string; command: null }>((resolve) => {
    finishLookup = resolve;
  });
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.create.mockReturnValue(createResponse);
  api.exact.mockReturnValue(lookupResponse);
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  await waitFor(() => expect(api.exact).toHaveBeenCalled());
  const sent = api.create.mock.calls[0]?.[0];
  if (!sent) throw new Error('create was not sent');
  await act(async () => {
    finishCreate({
      delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
      command: {
        commandId: sent.commandId,
        testId,
        status: CommandStatusStatus.CommandStatusStatusCompleted,
      },
    });
    await createResponse;
  });
  await screen.findByText(/Действие подтверждено ядром/);
  await act(async () => {
    finishLookup({ outcome: 'unknown', command: null });
    await lookupResponse;
  });
  expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
  expect(screen.getByText(/Действие подтверждено ядром/)).toBeTruthy();
  expect(api.create).toHaveBeenCalledOnce();
}, 10000);

it.each(['failure', 'invalid'])(
  'keeps exact acceptance after a late %s send response',
  async (mode) => {
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 0,
          lastEventSequence: 0,
          measurementBatch: null,
          activeTest: null,
          pendingAction: null,
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    let finish: (value: unknown) => void = () => {};
    let fail: (value: Error) => void = () => {};
    const sent = new Promise((resolve, reject) => {
      finish = resolve;
      fail = reject;
    });
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    api.create.mockReturnValue(sent);
    api.exact.mockImplementation(
      async (_db: string, _kind: string, _test: string, _action: string, id: string) => ({
        outcome: 'match',
        command: { commandId: id, testId, status: 'accepted' },
      }),
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
    await screen.findByText(/Команда: принята; завершение команды не подтверждено/);
    await act(async () => {
      if (mode === 'failure') fail(new Error('lost'));
      else finish({ delivery: 'response', command: null });
      await sent.catch(() => {});
    });
    expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
    expect(screen.getByText(/Команда: принята; завершение команды не подтверждено/)).toBeTruthy();
  },
  10000,
);

it('accepts a valid direct receipt without testId', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.create.mockImplementation(async (input: CreateCommandInput) => ({
    delivery: 'response',
    command: { commandId: input.commandId, status: 'accepted' },
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
  await screen.findByText(/Команда: принята; завершение команды не подтверждено/);
  expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
});

it('links a completed Create without testId to its exact result and snapshot', async () => {
  let activeTest: Snapshot['activeTest'] = null;
  let revision = 0;
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: revision + 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision,
        lastEventSequence: revision,
        activeTest,
        measurementBatch: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.readiness.mockResolvedValue({ testId, revision: 1, ready: true, checks: [] });
  api.create.mockImplementation(async (input: CreateCommandInput) => ({
    delivery: 'response',
    command: { commandId: input.commandId, status: 'completed' },
  }));
  let finishExact: (value: {
    outcome: string;
    command: { commandId: string; testId: string; status: string };
  }) => void = () => {};
  const exactResponse = new Promise<{
    outcome: string;
    command: { commandId: string; testId: string; status: string };
  }>((resolve) => {
    finishExact = resolve;
  });
  api.exact.mockReturnValue(exactResponse);
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  await waitFor(() => expect(api.exact).toHaveBeenCalled());
  await screen.findByText(/Команда: выполнена/);
  const sent = api.create.mock.calls[0]?.[0];
  if (!sent) throw new Error('create was not sent');
  await act(async () => {
    finishExact({
      outcome: 'match',
      command: { commandId: sent.commandId, testId, status: 'completed' },
    });
    await exactResponse;
  });
  activeTest = record;
  revision = 1;
  await screen.findByRole('heading', { name: 'Автоматические проверки' }, { timeout: 8000 });
  await waitFor(() => expect(screen.queryByText(/Команда: выполнена/)).toBeNull(), {
    timeout: 8000,
  });
  expect(api.create).toHaveBeenCalledOnce();
}, 10000);

it('checks an exact ordinary attempt even when its ID is reserved for Stop', async () => {
  const oldTest = '77777777-7777-4777-8777-777777777777';
  const attempt = savedAttempt(stopId, 'start', oldTest, false);
  sessionStorage.setItem(pendingKey, JSON.stringify([attempt]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.exact.mockResolvedValue({ outcome: 'conflict', command: null });
  api.stop.mockResolvedValue(stopResponse({ commandId: stopId, testId, status: 'completed' }));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await screen.findByText(/Идентификатор принадлежит другой команде/);
  expect(api.exact).toHaveBeenCalledWith(
    database,
    'start',
    oldTest,
    '',
    stopId,
    attempt.originalJSON,
  );
  await userEvent.setup().click(screen.getByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText('Остановка подтверждена ядром.');
  expect(api.stop).toHaveBeenCalledOnce();
});

it('can confirm Stop while an exact lookup with the same ID is waiting', async () => {
  const attempt = savedAttempt(stopId, 'start', testId, false);
  sessionStorage.setItem(pendingKey, JSON.stringify([attempt]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finish: (value: { outcome: string; command: null }) => void = () => {};
  const held = new Promise<{ outcome: string; command: null }>((resolve) => {
    finish = resolve;
  });
  api.exact.mockImplementation(async (_db: string, kind: string) =>
    kind === 'stop'
      ? { outcome: 'match', command: { commandId: stopId, testId, status: 'completed' } }
      : held,
  );
  api.stop.mockResolvedValue(stopResponse({ commandId: stopId, testId, status: 'accepted' }));
  const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await waitFor(() => expect(api.exact).toHaveBeenCalled());
  await userEvent.setup().click(screen.getByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText('Остановка подтверждена ядром.');
  expect(api.exact).toHaveBeenCalledWith(
    database,
    'stop',
    testId,
    '',
    stopId,
    JSON.stringify({ commandId: stopId }),
  );
  expect(api.stop).toHaveBeenCalledOnce();
  view.unmount();
  await act(async () => {
    finish({ outcome: 'unknown', command: null });
    await held;
  });
});

it('keeps the first unresolved Stop when a later Stop attempt is rejected', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.stop
    .mockRejectedValueOnce(new Error('first response lost'))
    .mockResolvedValueOnce({ delivery: 'conflict', command: null });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText(/Исход остановки неизвестен/);
  await user.click(screen.getByRole('button', { name: 'Повторить запрос остановки' }));
  await screen.findByText(/Запрос остановки отклонён/);
  const stored = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
  expect(stored).toContainEqual(
    expect.objectContaining({
      id: stopId,
      kind: 'stop',
      testId,
      originalJSON: JSON.stringify({ commandId: stopId }),
    }),
  );
  expect(api.stop).toHaveBeenCalledTimes(2);
  await user.click(screen.getByRole('button', { name: 'Прекратить наблюдение' }));
  expect(screen.queryByText(/Запрос остановки отклонён/)).toBeNull();
});

it('shows the restored terminal Stop status when the operator requests it again', async () => {
  sessionStorage.setItem(pendingKey, JSON.stringify([savedAttempt(stopId, 'stop', testId, false)]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({
    outcome: 'match',
    command: { commandId: stopId, testId, status: 'completed' },
  });
  let finish: (value: { commandId: string; testId: string; status: string }) => void = () => {};
  const response = new Promise<{ commandId: string; testId: string; status: string }>((resolve) => {
    finish = resolve;
  });
  api.stop.mockReturnValue(response.then(stopResponse));
  const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await screen.findByText(/Команда: выполнена/);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  expect(screen.getByText('Остановка подтверждена ядром.')).toBeTruthy();
  view.unmount();
  await act(async () => {
    finish({ commandId: stopId, testId, status: 'completed' });
    await response;
  });
});

it.each(['failed', 'rejected'])(
  'keeps a confirmed %s Stop visible during a retry',
  async (status) => {
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 2,
          lastEventSequence: 2,
          measurementBatch: null,
          activeTest: { ...record, stage: TestRecordStage.Running },
          pendingAction: null,
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
    let finishRetry: (value: { commandId: string; testId: string; status: string }) => void =
      () => {};
    const retry = new Promise<{ commandId: string; testId: string; status: string }>((resolve) => {
      finishRetry = resolve;
    });
    api.stop
      .mockResolvedValueOnce(stopResponse({ commandId: stopId, testId, status }))
      .mockReturnValueOnce(retry.then(stopResponse));
    const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    expect(await screen.findByText(/Остановка не выполнена или отклонена/)).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Повторить запрос остановки' }));
    expect(screen.getByText(/Остановка не выполнена или отклонена/)).toBeTruthy();
    view.unmount();
    await act(async () => {
      finishRetry({ commandId: stopId, testId, status: 'unknown' });
      await retry;
    });
  },
);

it('does not let a late rejected Stop replace a confirmed finished outcome', async () => {
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let rejectSecond: (reason: Error) => void = () => {};
  const second = new Promise<never>((_resolve, reject) => {
    rejectSecond = reject;
  });
  const completed = { commandId: stopId, testId, status: 'completed' };
  api.exact.mockResolvedValue({ outcome: 'match', command: completed });
  api.stop
    .mockResolvedValueOnce(stopResponse(completed))
    .mockReturnValueOnce(second.then(stopResponse));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText('Остановка подтверждена ядром.');
  await user.click(screen.getByRole('button', { name: 'Повторить запрос остановки' }));
  snapshot = {
    ...snapshot,
    revision: 3,
    lastEventSequence: 3,
    measurementBatch: null,
    activeTest: {
      ...record,
      stage: TestRecordStage.Finished,
      outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
    },
  };
  await waitFor(() => expect(screen.queryByText(/Команда: выполнена/)).toBeNull(), {
    timeout: 8000,
  });
  await act(async () => {
    rejectSecond(new CommandConflictError());
    await second.catch(() => {});
  });
  expect(screen.queryByText(/Запрос остановки отклонён/)).toBeNull();
  expect(screen.getByText('Остановка подтверждена ядром.')).toBeTruthy();
}, 12000);

it('does not discard a Stop observation while an earlier request is still in flight', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let rejectFirst: (reason: Error) => void = () => {};
  const first = new Promise<never>((_resolve, reject) => {
    rejectFirst = reject;
  });
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.stop
    .mockReturnValueOnce(first)
    .mockResolvedValueOnce({ delivery: 'conflict', command: null });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await user.click(screen.getByRole('button', { name: 'Повторить запрос остановки' }));
  await screen.findByText(/Запрос остановки отклонён/);
  const hide = screen.getByRole('button', { name: 'Прекратить наблюдение' });
  expect(hide.hasAttribute('disabled')).toBe(true);
  await act(async () => {
    rejectFirst(new Error('response lost'));
    await first.catch(() => {});
  });
  await waitFor(() => expect(hide.hasAttribute('disabled')).toBe(false));
  expect(JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]')).toHaveLength(1);
});

it.each(['conflict', 'wrongTest', 'badStatus'])('does not confirm Stop from %s', async (mode) => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockResolvedValue({
    commandId: stopId,
    testId: '77777777-7777-4777-8777-777777777777',
    status: 'completed',
  });
  if (mode === 'conflict') api.stop.mockResolvedValue({ delivery: 'conflict', command: null });
  else if (mode === 'wrongTest')
    api.stop.mockResolvedValue(
      stopResponse({
        commandId: stopId,
        testId: '77777777-7777-4777-8777-777777777777',
        status: 'completed',
      }),
    );
  else
    api.stop.mockResolvedValue(
      stopResponse({ commandId: stopId, testId, status: 'completed', reason: {} }),
    );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  if (mode === 'conflict') await screen.findByText(/запрос остановки отклонён.*идентификатор/i);
  else await screen.findByText(/Исход остановки неизвестен/);
  expect(screen.queryByText('Остановка подтверждена ядром.')).toBeNull();
  expect(screen.getAllByText(/безопасное давление не подтверждено/i).length).toBeGreaterThan(0);
  expect(api.stop).toHaveBeenCalledOnce();
});

it('allows an explicitly reviewed legacy observation to be removed from a full list', async () => {
  const old = Array.from({ length: 64 }, (_, index) => ({
    id: `old-${index}`,
    kind: 'create',
    testId: null,
    retired: true,
    legacyReviewed: true,
  }));
  sessionStorage.setItem(pendingKey, JSON.stringify(old));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const remove = (await screen.findAllByRole('button', { name: 'Удалить запись наблюдения' }))[0];
  if (!remove) throw new Error('reviewed observation is missing');
  const failedWrite = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  await userEvent.setup().click(remove);
  expect(screen.getByText(/ID old-0\./)).toBeTruthy();
  expect(JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]')).toHaveLength(64);
  failedWrite.mockRestore();
  await userEvent.setup().click(remove);
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
      false,
    ),
  );
  expect(JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]')).toHaveLength(63);
  expect(screen.queryByText(/ID old-0\./)).toBeNull();
  expect(api.command).not.toHaveBeenCalled();
});

it.each(['start', 'confirm'] as const)(
  'uses the original %s attempt and path after remount without sending it again',
  async (kind) => {
    const id = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
    const attempt = savedAttempt(id, kind, testId, false);
    sessionStorage.setItem(pendingKey, JSON.stringify([attempt]));
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 2,
          lastEventSequence: 2,
          measurementBatch: null,
          activeTest: { ...record, stage: TestRecordStage.Running },
          pendingAction: { testId, actionId, instruction: 'Подтвердите измерение' },
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
    const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        database,
        kind,
        testId,
        kind === 'confirm' ? actionId : '',
        id,
        attempt.originalJSON,
      ),
    );
    view.unmount();
    api.exact.mockClear();
    api.exact.mockResolvedValue({
      outcome: 'match',
      command: { commandId: id, testId, status: 'completed' },
    });
    render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        database,
        kind,
        testId,
        kind === 'confirm' ? actionId : '',
        id,
        attempt.originalJSON,
      ),
    );
    if (kind === 'start') {
      await waitFor(() => expect(screen.queryByText(new RegExp(`Команда:.*${id}`))).toBeNull());
      await waitFor(() =>
        expect(
          screen.getByRole('button', { name: 'Подтвердить действие' }).hasAttribute('disabled'),
        ).toBe(false),
      );
    } else await screen.findByText(/Команда: выполнена/);
    expect(api.start).not.toHaveBeenCalled();
    expect(api.confirm).not.toHaveBeenCalled();
  },
);

it('does not report an unpersisted Stop when an ordinary browser receipt cannot be saved', async () => {
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([{ id: stopId, kind: 'stop', testId, retired: true }]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const originalSet = Storage.prototype.setItem;
  let writes = 0;
  const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (
    this: Storage,
    key: string,
    value: string,
  ) {
    if (key === pendingKey && ++writes > 1) throw new Error('storage unavailable');
    return originalSet.call(this, key, value);
  });
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.create.mockImplementation(async (input: CreateCommandInput) => ({
    delivery: CommandDispatchDelivery.CommandDispatchDeliveryResponse,
    command: {
      commandId: input.commandId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
    },
  }));
  api.command.mockRejectedValue(new Error('status unavailable'));
  const browser = { ...desktopDemoService };
  render(<LiveApp session={bridge} service={browser} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  await screen.findByText(/Не удалось обновить список команд/);
  expect(screen.queryByText(/Исход остановки отслеживается только в памяти/)).toBeNull();
  expect(api.stop).not.toHaveBeenCalled();
  setItem.mockRestore();
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    testId,
    status: CommandStatusStatus.CommandStatusStatusRejected,
  }));
  const checks = screen.getAllByRole('button', { name: 'Проверить состояние' });
  const createCheck = checks.at(-1);
  if (!createCheck) throw new Error('creation status check missing');
  await user.click(createCheck);
  await waitFor(() => expect(screen.queryByText(/Не удалось обновить список команд/)).toBeNull());
}, 10000);

it.each(['rejected response', 'not sent response'] as const)(
  'clears the unknown creation message after the same command is confirmed: %s',
  async (lateOutcome) => {
    let snapshot: Snapshot = {
      apiVersion: 'v1',
      backendRunId: backend,
      revision: 0,
      lastEventSequence: 0,
      measurementBatch: null,
      activeTest: null,
      pendingAction: null,
      measurementTiming: null,
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
          leaseAcquireUnknown: false,
          streamGeneration: 0,
        },
        snapshot,
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    let allowRead = false;
    let rejectCreate: (error: Error) => void = () => {};
    let finishCreate: (value: CommandDispatch) => void = () => {};
    const delayedCreate = new Promise<CommandDispatch>((resolve, reject) => {
      finishCreate = resolve;
      rejectCreate = reject;
    });
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    api.create.mockReturnValue(delayedCreate);
    api.command.mockImplementation(async (id: string) => {
      if (!allowRead) throw new Error('temporarily unavailable');
      return { commandId: id, testId, status: CommandStatusStatus.CommandStatusStatusCompleted };
    });
    api.readiness.mockResolvedValue({ testId, revision: 1, ready: true, checks: [] });
    render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    const user = userEvent.setup();
    const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
    await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
    await user.click(newTest);
    await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
    await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
    await user.type(screen.getByLabelText(/производителя, Па/), '10000');
    await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
    await screen.findByText(/Исход команды пока неизвестен/);
    allowRead = true;
    await user.click(screen.getByRole('button', { name: 'Проверить состояние' }));
    await screen.findByText(/Действие подтверждено ядром; ожидаем состояние/);
    await act(async () => {
      if (lateOutcome === 'rejected response') rejectCreate(new Error('response lost'));
      else
        finishCreate({
          delivery: CommandDispatchDelivery.CommandDispatchDeliveryNotSent,
          command: null,
        });
      await delayedCreate.catch(() => {});
    });
    expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
    expect(screen.getByText(/Команда: выполнена/)).toBeTruthy();
    expect(
      screen.getByRole('button', { name: 'Создание испытания…' }).hasAttribute('disabled'),
    ).toBe(true);
    snapshot = { ...snapshot, revision: 1, lastEventSequence: 1, activeTest: record };
    await waitFor(() => expect(screen.queryByText(/Команда: выполнена/)).toBeNull(), {
      timeout: 8000,
    });
    expect(screen.queryByText(/Действие подтверждено ядром; ожидаем состояние/)).toBeNull();
    expect(api.create).toHaveBeenCalledOnce();
  },
  15000,
);

it('does not restore an unknown message after a later successful lookup', async () => {
  const createId = '66666666-6666-4666-8666-666666666666';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([savedAttempt(createId, 'create', null, false)]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let rejectFirst: (error: Error) => void = () => {};
  const first = new Promise<never>((_resolve, reject) => {
    rejectFirst = reject;
  });
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockReturnValueOnce(first).mockResolvedValue({
    commandId: createId,
    testId,
    status: CommandStatusStatus.CommandStatusStatusCompleted,
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await waitFor(() => expect(api.command).toHaveBeenCalledOnce());
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить состояние' }));
  await screen.findByText(/Команда: выполнена/);
  await act(async () => {
    rejectFirst(new Error('late failure'));
    await first.catch(() => {});
  });
  expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
  expect(screen.getByText(/Команда: выполнена/)).toBeTruthy();
  expect(api.create).not.toHaveBeenCalled();
});

it('keeps an unknown stop message when a different command is confirmed', async () => {
  const createId = '66666666-6666-4666-8666-666666666666';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([
      savedAttempt(stopId, 'stop', testId, false),
      savedAttempt(createId, 'create', null, false),
    ]),
  );
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.readiness.mockResolvedValue({ testId, revision: 1, ready: true, checks: [] });
  api.command.mockImplementation(async (id: string) => {
    if (id === stopId) throw new Error('stop status unavailable');
    return { commandId: id, testId, status: CommandStatusStatus.CommandStatusStatusCompleted };
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await screen.findByText(/Исход команды пока неизвестен/);
  const createCheck = screen.getAllByRole('button', { name: 'Проверить состояние' })[1];
  if (!createCheck) throw new Error('create status check missing');
  await userEvent.setup().click(createCheck);
  await screen.findByText(/Команда: выполнена/);
  expect(screen.getByText(/Исход команды пока неизвестен/)).toBeTruthy();
  expect(screen.getByText(new RegExp(`Остановка испытания ${testId}`))).toBeTruthy();
  snapshot = { ...snapshot, revision: 1, lastEventSequence: 1, activeTest: record };
  await waitFor(() => expect(screen.queryByText(/Команда: выполнена/)).toBeNull(), {
    timeout: 8000,
  });
  expect(screen.getByText(/Исход команды пока неизвестен/)).toBeTruthy();
  expect(screen.getByText(new RegExp(`Остановка испытания ${testId}`))).toBeTruthy();
  expect(api.create).not.toHaveBeenCalled();
}, 15000);

it('keeps another command unknown when an earlier command fails', async () => {
  const createId = '66666666-6666-4666-8666-666666666666';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([
      savedAttempt(stopId, 'stop', testId, true),
      savedAttempt(createId, 'create', null, false),
    ]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finishStop: (value: {
    commandId: string;
    testId: string;
    status: CommandStatusStatus;
  }) => void = () => {};
  const stop = new Promise<{ commandId: string; testId: string; status: CommandStatusStatus }>(
    (resolve) => {
      finishStop = resolve;
    },
  );
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockImplementation(async (id: string) => {
    if (id === stopId) return stop;
    throw new Error('creation status unavailable');
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await waitFor(() => expect(api.command).toHaveBeenCalledWith(stopId));
  const createCheck = screen.getAllByRole('button', { name: 'Проверить состояние' })[1];
  if (!createCheck) throw new Error('create status check missing');
  await userEvent.setup().click(createCheck);
  await screen.findByText(/Исход команды пока неизвестен/);
  await act(async () => {
    finishStop({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusFailed,
    });
    await stop;
  });
  expect(
    await screen.findByText(new RegExp(`Испытание ${testId}: Остановка не выполнена`)),
  ).toBeTruthy();
  expect(screen.getByText(/Исход команды пока неизвестен/)).toBeTruthy();
  expect(api.create).not.toHaveBeenCalled();
});

it('keeps separate failed stop results visible until the operator hides them', async () => {
  const stops = [
    { id: stopId, testId },
    { id: '66666666-6666-4666-8666-666666666666', testId: '77777777-7777-4777-8777-777777777777' },
    { id: '88888888-8888-4888-8888-888888888888', testId: '99999999-9999-4999-8999-999999999999' },
  ];
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify(stops.map((item) => savedAttempt(item.id, 'stop', item.testId, true))),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    testId: stops.find((item) => item.id === id)?.testId,
    status: CommandStatusStatus.CommandStatusStatusFailed,
  }));
  const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  for (const item of stops) {
    const row = await screen.findByText(new RegExp(`ID ${item.id}`));
    await user.click(within(row).getByRole('button', { name: 'Проверить состояние' }));
    await waitFor(() => expect(row.textContent).toContain('завершилась с ошибкой'));
  }
  for (const item of stops) {
    const row = screen.getByText(new RegExp(`ID ${item.id}`));
    expect(row.textContent).toContain(`Остановка испытания ${item.testId}`);
    expect(row.textContent).toContain('Безопасное давление не подтверждено');
  }
  const first = screen.getByText(new RegExp(`ID ${stops[0]?.id}`));
  await user.click(within(first).getByRole('button', { name: 'Скрыть наблюдение команды' }));
  expect(screen.queryByText(new RegExp(`ID ${stops[0]?.id}`))).toBeNull();
  for (const item of stops.slice(1))
    expect(screen.getByText(new RegExp(`ID ${item.id}`))).toBeTruthy();
  view.unmount();
  api.command.mockRejectedValue(new Error('core unavailable'));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  for (const item of stops.slice(1)) {
    const row = await screen.findByText(new RegExp(`ID ${item.id}`));
    expect(row.textContent).toContain('Безопасное давление не подтверждено');
    expect(row.textContent).toContain('завершилась с ошибкой');
  }
  expect(api.stop).not.toHaveBeenCalled();
});

it.each(['notSent', 'rejected', 'failed', 'lookupRejected', 'storageUnavailable'] as const)(
  'clears an earlier %s notice when the operator retries successfully',
  async (firstOutcome) => {
    let snapshot: Snapshot = {
      apiVersion: 'v1',
      backendRunId: backend,
      revision: 0,
      lastEventSequence: 0,
      measurementBatch: null,
      activeTest: null,
      pendingAction: null,
      measurementTiming: null,
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
          leaseAcquireUnknown: false,
          streamGeneration: 0,
        },
        snapshot,
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    const ids: string[] = [];
    api.list.mockResolvedValue({ items: [], nextAfter: null });
    api.command.mockImplementation(async (id: string) => ({
      commandId: id,
      status:
        firstOutcome === 'lookupRejected' && id === ids[0]
          ? CommandStatusStatus.CommandStatusStatusRejected
          : CommandStatusStatus.CommandStatusStatusUnknown,
    }));
    api.create.mockImplementation(async (input: CreateCommandInput) => {
      ids.push(input.commandId);
      if (ids.length === 1 && firstOutcome !== 'storageUnavailable') {
        if (firstOutcome === 'notSent') return { delivery: 'notSent', command: null };
        if (firstOutcome === 'lookupRejected') throw new Error('response lost');
        return {
          delivery: 'response',
          command: { commandId: input.commandId, status: firstOutcome },
        };
      }
      snapshot = { ...snapshot, activeTest: record, revision: 1, lastEventSequence: 1 };
      return {
        delivery: 'response',
        command: {
          commandId: input.commandId,
          testId,
          status: CommandStatusStatus.CommandStatusStatusCompleted,
        },
      };
    });
    api.readiness.mockResolvedValue({ testId, revision: 1, ready: true, checks: [] });
    render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    const user = userEvent.setup();
    const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
    await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
    await user.click(newTest);
    await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
    await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
    await user.type(screen.getByLabelText(/производителя, Па/), '10000');
    const storage =
      firstOutcome === 'storageUnavailable'
        ? vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
            throw new Error('storage unavailable');
          })
        : null;
    await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
    const oldNotice =
      firstOutcome === 'storageUnavailable'
        ? /Не удалось сохранить идентификатор/
        : firstOutcome === 'notSent'
          ? /Команда не отправлена:/
          : /Команда отклонена/;
    await screen.findByText(oldNotice);
    storage?.mockRestore();
    if (firstOutcome === 'storageUnavailable') {
      await user.click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    }
    const retry = screen.getByRole('button', { name: /Проверить готовность/ });
    await waitFor(() => expect(retry.hasAttribute('disabled')).toBe(false));
    await user.click(retry);
    await screen.findByRole('heading', { name: 'Готовность' }, { timeout: 8000 });
    expect(screen.queryByText(oldNotice)).toBeNull();
    expect(ids).toHaveLength(firstOutcome === 'storageUnavailable' ? 1 : 2);
    if (ids.length === 2) expect(ids[0]).not.toBe(ids[1]);
  },
  12000,
);

it('keeps an earlier test command failure when creating an unrelated test', async () => {
  const oldStartId = '66666666-6666-4666-8666-666666666666';
  const oldTestId = '77777777-7777-4777-8777-777777777777';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([savedAttempt(oldStartId, 'start', oldTestId, true)]),
  );
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockImplementation(async (id: string) => ({
    commandId: id,
    testId: id === oldStartId ? oldTestId : testId,
    status:
      id === oldStartId
        ? CommandStatusStatus.CommandStatusStatusFailed
        : CommandStatusStatus.CommandStatusStatusUnknown,
  }));
  api.create.mockImplementation(async (input: CreateCommandInput) => {
    snapshot = { ...snapshot, activeTest: record, revision: 1, lastEventSequence: 1 };
    return {
      delivery: 'response',
      command: {
        commandId: input.commandId,
        testId,
        status: CommandStatusStatus.CommandStatusStatusCompleted,
      },
    };
  });
  api.readiness.mockRejectedValue(new Error('readiness unavailable'));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const previousNotice = await screen.findByText(/Команда отклонена или не выполнена/);
  const noticeRegion = previousNotice.closest('.demo-command-message');
  if (!noticeRegion) throw new Error('command message region missing');
  noticeRegion.scrollTop = 100;
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  const newNotice = await screen.findByText(
    'Не удалось подтвердить готовность.',
    {},
    { timeout: 8000 },
  );
  const oldNotice = screen.getByText(/Команда отклонена или не выполнена/);
  expect(oldNotice.textContent).toContain(oldStartId);
  expect(oldNotice.textContent).toContain(oldTestId);
  expect(newNotice.compareDocumentPosition(oldNotice) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(
    0,
  );
  expect(noticeRegion.scrollTop).toBe(0);
  expect(api.create).toHaveBeenCalledTimes(1);
}, 12000);

it('brings a repeated readiness error back into view after another notice', async () => {
  const oldStartId = '66666666-6666-4666-8666-666666666666';
  const oldTestId = '77777777-7777-4777-8777-777777777777';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([savedAttempt(oldStartId, 'start', oldTestId, true)]),
  );
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 1,
    lastEventSequence: 1,
    measurementBatch: null,
    activeTest: record,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finishOld: (status: {
    commandId: string;
    testId: string;
    status: CommandStatusStatus;
  }) => void = () => {};
  const old = new Promise<{ commandId: string; testId: string; status: CommandStatusStatus }>(
    (resolve) => {
      finishOld = resolve;
    },
  );
  let failReadiness: (error: Error) => void = () => {};
  const nextReadiness = new Promise<Readiness>((_resolve, reject) => {
    failReadiness = reject;
  });
  api.list.mockResolvedValue({ items: [], nextAfter: null });
  api.command.mockReturnValue(old);
  api.readiness.mockRejectedValueOnce(new Error('unavailable')).mockReturnValue(nextReadiness);
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await screen.findByText('Не удалось подтвердить готовность.');
  await waitFor(() => expect(api.command).toHaveBeenCalledWith(oldStartId));
  await act(async () => {
    finishOld({
      commandId: oldStartId,
      testId: oldTestId,
      status: CommandStatusStatus.CommandStatusStatusFailed,
    });
    await old;
  });
  const oldNotice = await screen.findByText(/Команда отклонена или не выполнена/);
  const noticeRegion = oldNotice.closest('.demo-command-message');
  if (!noticeRegion) throw new Error('command message region missing');
  await waitFor(() => expect(api.readiness.mock.calls.length).toBeGreaterThan(1), {
    timeout: 6000,
  });
  noticeRegion.scrollTop = 100;
  await act(async () => {
    failReadiness(new Error('still unavailable'));
    await nextReadiness.catch(() => {});
  });
  const repeated = screen.getByText('Не удалось подтвердить готовность.');
  expect(repeated.compareDocumentPosition(oldNotice) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(
    0,
  );
  expect(noticeRegion.scrollTop).toBe(0);
}, 12000);

it('keeps a confirmed not-sent result when the service wrapper changes', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 0,
    lastEventSequence: 0,
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
  const service = { ...desktopDemoService };
  const replacement = { ...desktopDemoService };
  const view = render(
    <LiveApp session={bridge} service={service} hostRunId={host} backendRunId={backend} />,
  );
  const user = userEvent.setup();
  const newTest = await screen.findByRole('button', { name: 'Новое испытание' });
  await waitFor(() => expect(newTest.hasAttribute('disabled')).toBe(false));
  await user.click(newTest);
  await user.type(screen.getByLabelText('Обозначение или наименование клапана'), 'К-1');
  await user.type(screen.getByLabelText('Площадь клапана, квадратные метры'), '0,012');
  await user.type(screen.getByLabelText(/производителя, Па/), '10000');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  await screen.findByText(/Команда: исход неизвестен/);
  await waitFor(() => expect(api.command).toHaveBeenCalled());
  const initialLookup = api.command.mock.results[0]?.value;
  expect(initialLookup).toBeDefined();
  await act(async () => {
    await initialLookup;
  });
  view.rerender(
    <LiveApp session={bridge} service={replacement} hostRunId={host} backendRunId={backend} />,
  );
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
    pendingKey,
    JSON.stringify([{ id: startId, kind: 'start', testId, retired: false }]),
  );
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: { testId, actionId, instruction: 'Подтвердите измерение' },
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusFailed,
      testId,
      reason: 'simulator_stop_failed',
    }),
  );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const user = userEvent.setup();
  const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
  await user.click(stop);
  expect(api.stop).toHaveBeenCalledTimes(1);
  expect(api.stop).toHaveBeenCalledWith(testId, { commandId: stopId });
  await screen.findByText(/Остановка не выполнена или отклонена/);
});

it('does not report an unknown stop after its confirmed response', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let rejectLookup: (error: Error) => void = () => {};
  const lookup = new Promise<never>((_resolve, reject) => {
    rejectLookup = reject;
  });
  let finishStop: (value: {
    commandId: string;
    testId: string;
    status: CommandStatusStatus;
  }) => void = () => {};
  const stop = new Promise<{ commandId: string; testId: string; status: CommandStatusStatus }>(
    (resolve) => {
      finishStop = resolve;
    },
  );
  api.command.mockReturnValue(lookup);
  api.stop.mockReturnValue(stop.then(stopResponse));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await waitFor(() => expect(api.command).toHaveBeenCalledWith(stopId));
  await act(async () => {
    finishStop({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    });
    await stop;
  });
  await screen.findByText('Остановка подтверждена ядром.');
  await act(async () => {
    rejectLookup(new Error('late failure'));
    await lookup.catch(() => {});
  });
  expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
  expect(screen.getByText('Остановка подтверждена ядром.')).toBeTruthy();
  expect(screen.getByText(/Команда: выполнена/)).toBeTruthy();
  expect(api.stop).toHaveBeenCalledOnce();
});

it('keeps a storage warning when stop fails', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finishLookup: (value: { commandId: string; status: CommandStatusStatus }) => void = () => {};
  const heldLookup = new Promise<{ commandId: string; status: CommandStatusStatus }>((resolve) => {
    finishLookup = resolve;
  });
  api.command.mockReturnValue(heldLookup);
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusFailed,
    }),
  );
  const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  expect(await screen.findByText(/Остановка не выполнена или отклонена/)).toBeTruthy();
  expect(screen.getByText(/Не удалось обновить список команд в интерфейсе/)).toBeTruthy();
  setItem.mockRestore();
  api.command.mockResolvedValue({
    commandId: stopId,
    testId,
    status: CommandStatusStatus.CommandStatusStatusFailed,
  });
  await userEvent.setup().click(screen.getByRole('button', { name: 'Проверить состояние' }));
  await waitFor(() =>
    expect(screen.queryByText(/Не удалось обновить список команд в интерфейсе/)).toBeNull(),
  );
  view.unmount();
  await act(async () => {
    finishLookup({ commandId: stopId, status: CommandStatusStatus.CommandStatusStatusUnknown });
    await heldLookup;
  });
});

it('clears an unknown stop when the separate stop check confirms it', async () => {
  const oldId = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd';
  sessionStorage.setItem(pendingKey, JSON.stringify([savedAttempt(oldId, 'start', testId, true)]));
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  let finishOld: (value: { commandId: string; status: CommandStatusStatus }) => void = () => {};
  const old = new Promise<{ commandId: string; status: CommandStatusStatus }>((resolve) => {
    finishOld = resolve;
  });
  let finishStop: (value: {
    commandId: string;
    testId: string;
    status: CommandStatusStatus;
  }) => void = () => {};
  const stopCheck = new Promise<{ commandId: string; testId: string; status: CommandStatusStatus }>(
    (resolve) => {
      finishStop = resolve;
    },
  );
  let stopReads = 0;
  let allowConfirmation = false;
  api.command.mockImplementation(async (id: string) => {
    if (id === oldId) return old;
    stopReads++;
    if (!allowConfirmation) throw new Error('temporarily unavailable');
    return stopCheck;
  });
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
    }),
  );
  const view = render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await waitFor(() => expect(api.command).toHaveBeenCalledWith(oldId));
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  const stopCheckButton = screen.getAllByRole('button', { name: 'Проверить состояние' })[1];
  if (!stopCheckButton) throw new Error('stop status check missing');
  await userEvent.setup().click(stopCheckButton);
  await screen.findByText(/Исход команды пока неизвестен/);
  const previousReads = stopReads;
  allowConfirmation = true;
  snapshot = {
    ...snapshot,
    lastEventSequence: 3,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
  };
  await waitFor(() => expect(stopReads).toBeGreaterThan(previousReads), { timeout: 8000 });
  await act(async () => {
    finishStop({
      commandId: stopId,
      testId,
      status: CommandStatusStatus.CommandStatusStatusCompleted,
    });
    await stopCheck;
  });
  expect(await screen.findByText('Остановка подтверждена ядром.')).toBeTruthy();
  expect(screen.queryByText(/Исход команды пока неизвестен/)).toBeNull();
  expect(screen.getByText(new RegExp(`Команда: выполнена.*${stopId}`))).toBeTruthy();
  view.unmount();
  await act(async () => {
    finishOld({ commandId: oldId, status: CommandStatusStatus.CommandStatusStatusUnknown });
    await old;
  });
}, 15000);

it('explains when a pending start was canceled by stop', async () => {
  const startId = '66666666-6666-4666-8666-666666666666';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([savedAttempt(startId, 'start', testId, false)]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockResolvedValue({
    commandId: startId,
    status: CommandStatusStatus.CommandStatusStatusFailed,
    testId,
    reason: 'canceled_by_stop',
  });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  expect(
    await screen.findByText('Запуск отменён при остановке испытания до подтверждения оператора.'),
  ).toBeTruthy();
  expect(api.start).not.toHaveBeenCalled();
});

it('offers the reserved stop after remount even when heartbeat cannot be confirmed', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 0,
      linkValidForMs: 0,
    }),
  };
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    }),
  );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
  expect(screen.getByText('Связь не подтверждена')).toBeTruthy();
  await userEvent.setup().click(stop);
  expect(api.stop).toHaveBeenCalledWith(testId, { commandId: stopId });
  expect(api.start).not.toHaveBeenCalled();
});

it('does not carry an active snapshot into a different database before a new observation', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
    measurements: [],
  };
  const current: SessionView = {
    connection: {
      hostRunId: host,
      revision: 2,
      status: ConnectionStateStatus.Live,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    },
    snapshot,
    validForMs: 6000,
    linkValidForMs: 6000,
  };
  const never = new Promise<SessionView>(() => {});
  let currentRead: Promise<SessionView> = Promise.resolve(current);
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: () => currentRead,
  };
  const mounted = render(
    <LiveApp session={bridge} hostRunId={host} backendRunId={backend} databaseId={database} />,
  );
  await screen.findByRole('button', { name: 'Остановить испытание' });
  currentRead = never;
  mounted.rerender(
    <LiveApp
      session={bridge}
      hostRunId={host}
      backendRunId={backend}
      databaseId="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    />,
  );
  expect(screen.queryByRole('button', { name: 'Остановить испытание' })).toBeNull();
  expect(api.stop).not.toHaveBeenCalled();
});

it('does not reuse a previous database snapshot after full remount', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
    measurements: [],
  };
  const current: SessionView = {
    connection: {
      hostRunId: host,
      revision: 2,
      status: ConnectionStateStatus.Live,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    },
    snapshot,
    validForMs: 6000,
    linkValidForMs: 6000,
  };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => current,
  };
  const first = render(
    <LiveApp session={bridge} hostRunId={host} backendRunId={backend} databaseId={database} />,
  );
  await screen.findByRole('button', { name: 'Остановить испытание' });
  first.unmount();
  const never = new Promise<SessionView>(() => {});
  const replacement = { ...bridge, sessionView: () => never };
  const second = render(
    <LiveApp
      session={replacement}
      hostRunId={host}
      backendRunId={backend}
      databaseId="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    />,
  );
  expect(screen.queryByRole('button', { name: 'Остановить испытание' })).toBeNull();
  second.unmount();
  render(
    <LiveApp
      session={replacement}
      hostRunId={host}
      backendRunId={backend}
      databaseId="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    />,
  );
  expect(screen.queryByRole('button', { name: 'Остановить испытание' })).toBeNull();
});

it('retains the running display and reserved Stop while replacing the session wrapper', async () => {
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
    measurements: [],
  };
  const current: SessionView = {
    connection: {
      hostRunId: host,
      revision: 2,
      status: ConnectionStateStatus.Live,
      linkValid: true,
      leaseAcquireUnknown: false,
      streamGeneration: 0,
    },
    snapshot,
    validForMs: 6000,
    linkValidForMs: 6000,
  };
  const first: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => current,
  };
  const mounted = render(<LiveApp session={first} hostRunId={host} backendRunId={backend} />);
  const chart = await screen.findByRole('img', { name: 'График показаний симулятора' });
  const never = new Promise<SessionView>(() => {});
  mounted.rerender(
    <LiveApp
      session={{ ...first, sessionView: () => never }}
      hostRunId={host}
      backendRunId={backend}
    />,
  );
  expect(screen.getByRole('img', { name: 'График показаний симулятора' })).toBe(chart);
  expect(screen.getByRole('button', { name: 'Остановить испытание' })).toBeTruthy();
});

it('continues checking Stop after the interrupted result and keeps saving separate', async () => {
  let snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    }),
  );
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
    measurementBatch: null,
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
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: older,
      validForMs: 6000,
      linkValidForMs: 6000,
    });
    await pending;
  });
  expect(screen.getByRole('button', { name: 'Остановить испытание' })).toBeTruthy();
  expect(screen.getByText('Связь не подтверждена')).toBeTruthy();
  expect(api.start).not.toHaveBeenCalled();
});

it('keeps an earlier unknown Stop observable when stopping a later test', async () => {
  const oldStop = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
  const nextTest = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
  const nextStop = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc';
  sessionStorage.setItem(
    pendingKey,
    JSON.stringify([{ id: oldStop, kind: 'stop', testId, retired: true }]),
  );
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 5,
    lastEventSequence: 5,
    measurementBatch: null,
    activeTest: {
      ...record,
      testId: nextTest,
      stopCommandId: nextStop,
      stage: TestRecordStage.Running,
    },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
  api.stop.mockReturnValue(new Promise(() => {}).then(stopResponse));
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  const saved: { id: string }[] = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
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
  sessionStorage.setItem(pendingKey, JSON.stringify(old));
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockReturnValue(new Promise(() => {}));
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    }),
  );
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  const saved: { id: string }[] = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
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
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    }),
  );
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

it('preserves a newer shared attempt when restoring an unpersisted Stop', async () => {
  const browserDatabase = '10101010-1010-4010-8010-101010101010';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const other = savedAttempt('20202020-2020-4020-8020-202020202020', 'create', null, false);
  const differentJSON = { ...other, originalJSON: `${other.originalJSON} ` };
  const old = savedAttempt('30303030-3030-4030-8030-303030303030', 'start', testId, true);
  const legacy = { id: 'legacy-other', kind: 'create', testId: null, retired: false };
  const transient = {
    id: 'legacy-removed',
    kind: 'create',
    testId: null,
    retired: true,
    legacyReviewed: true,
  };
  localStorage.setItem(key, JSON.stringify([old]));
  const snapshot: Snapshot = {
    apiVersion: 'v1',
    backendRunId: backend,
    revision: 2,
    lastEventSequence: 2,
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockImplementation(async (_database, kind) =>
    kind === 'stop'
      ? { outcome: 'match', command: { commandId: stopId, status: 'accepted', testId } }
      : { outcome: 'unknown', command: null },
  );
  api.stop.mockResolvedValue(stopResponse({ commandId: stopId, status: 'accepted', testId }));
  const originalSet = Storage.prototype.setItem;
  const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (
    this: Storage,
    key,
    value,
  ) {
    if (this === localStorage && key === `gk80.demo.pending-command.v1:${browserDatabase}`)
      throw new Error('unavailable');
    originalSet.call(this, key, value);
  });
  const props = {
    session: bridge,
    hostRunId: host,
    backendRunId: backend,
    databaseId: browserDatabase,
    pendingStorage: localStorage,
  };
  try {
    const mounted = render(<LiveApp {...props} />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    expect(screen.getByText(/отслеживается только в памяти/)).toBeTruthy();
    mounted.unmount();
    setItem.mockRestore();
    localStorage.setItem(key, JSON.stringify([other, differentJSON, legacy, transient]));
    const resumed = render(<LiveApp {...props} />);
    const failedRetry = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('still unavailable');
    });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    expect(screen.getByRole('button', { name: 'Повторить чтение команд' })).toBeTruthy();
    failedRetry.mockRestore();
    resumed.unmount();
    localStorage.setItem(key, JSON.stringify([other, differentJSON, legacy]));
    render(<LiveApp {...props} />);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        browserDatabase,
        'create',
        '',
        '',
        other.id,
        other.originalJSON,
      ),
    );
    await userEvent.setup().click(
      within(screen.getByText(new RegExp(`ID ${stopId}`))).getByRole('button', {
        name: 'Проверить состояние',
      }),
    );
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(
        browserDatabase,
        'stop',
        testId,
        '',
        stopId,
        JSON.stringify({ commandId: stopId }),
      ),
    );
    await waitFor(() => {
      const saved: { id: string; originalJSON?: string }[] = JSON.parse(
        localStorage.getItem(key) ?? '[]',
      );
      expect(saved.map((item) => item.id)).toEqual(expect.arrayContaining([stopId, other.id]));
      expect(saved.map((item) => item.id)).not.toContain(old.id);
      expect(saved.map((item) => item.id)).toContain(legacy.id);
      expect(saved.map((item) => item.id)).not.toContain(transient.id);
      expect(saved.filter((item) => item.id === other.id).map((item) => item.originalJSON)).toEqual(
        expect.arrayContaining([other.originalJSON, differentJSON.originalJSON]),
      );
    });
    expect(api.stop).toHaveBeenCalledTimes(1);
    expect(api.create).not.toHaveBeenCalled();
  } finally {
    setItem.mockRestore();
    localStorage.removeItem(key);
  }
});

it.each([
  'local',
  'other',
  'local-fails',
  'other-retired',
  'other-after-absence',
  'superseded',
  'superseded-no-version',
] as const)('retains a reactivated Stop after an unsuccessful removal (%s)', async (source) => {
  const browserDatabase =
    source === 'local'
      ? 'abababab-abab-4bab-8bab-abababababab'
      : source === 'other'
        ? 'cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd'
        : source === 'local-fails'
          ? 'efefefef-efef-4fef-8fef-efefefefefef'
          : source === 'other-retired'
            ? '12121212-1212-4212-8212-121212121212'
            : source === 'other-after-absence'
              ? '14141414-1414-4414-8414-141414141414'
              : source === 'superseded'
                ? '15151515-1515-4515-8515-151515151515'
                : '21212121-2121-4121-8121-212121212121';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const attempt = savedAttempt(stopId, 'stop', testId, true);
  localStorage.setItem(key, JSON.stringify([attempt]));
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const props = {
    session: bridge,
    hostRunId: host,
    backendRunId: backend,
    databaseId: browserDatabase,
    pendingStorage: localStorage,
  };
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.stop.mockRejectedValue(new Error('response lost'));
  const mounted = render(<LiveApp {...props} />);
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  const random =
    source === 'superseded-no-version'
      ? vi.spyOn(crypto, 'randomUUID').mockImplementation(() => {
          throw new Error('unavailable');
        })
      : null;
  try {
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Прекратить наблюдение' }));
    expect(screen.getByText(new RegExp(`ID ${stopId}`))).toBeTruthy();
    if (source !== 'local-fails' && source !== 'superseded' && source !== 'superseded-no-version')
      failure.mockRestore();
    if (
      source === 'local' ||
      source === 'local-fails' ||
      source === 'superseded' ||
      source === 'superseded-no-version'
    ) {
      await userEvent.setup().click(screen.getByRole('button', { name: 'Остановить испытание' }));
      expect(api.stop).toHaveBeenCalledOnce();
      if (
        source === 'local-fails' ||
        source === 'superseded' ||
        source === 'superseded-no-version'
      ) {
        mounted.unmount();
        failure.mockRestore();
        if (source === 'superseded' || source === 'superseded-no-version') {
          localStorage.setItem(
            key,
            JSON.stringify([
              {
                ...attempt,
                retired: false,
                observationVersion: '16161616-1616-4616-8616-161616161616',
              },
            ]),
          );
          const intermediate = render(<LiveApp {...props} />);
          intermediate.unmount();
        }
        random?.mockRestore();
        localStorage.setItem(key, '[]');
        render(<LiveApp {...props} />);
        await userEvent
          .setup()
          .click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
      }
    } else {
      mounted.unmount();
      if (source === 'other-after-absence') {
        localStorage.setItem(key, '[]');
        const intermediate = render(<LiveApp {...props} />);
        intermediate.unmount();
      }
      localStorage.setItem(
        key,
        JSON.stringify([
          {
            ...attempt,
            retired: source === 'other-retired' || source === 'other-after-absence',
            observationVersion: '13131313-1313-4313-8313-131313131313',
          },
        ]),
      );
      render(<LiveApp {...props} />);
      await userEvent
        .setup()
        .click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    }
    const saved: { id: string; retired: boolean }[] = JSON.parse(localStorage.getItem(key) ?? '[]');
    if (source === 'superseded' || source === 'superseded-no-version') {
      expect(saved).toEqual([]);
      expect(screen.queryByText(new RegExp(`ID ${stopId}`))).toBeNull();
      return;
    }
    expect(saved).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: stopId,
          retired: source === 'other-retired' || source === 'other-after-absence',
        }),
      ]),
    );
    expect(screen.getByText(new RegExp(`ID ${stopId}`))).toBeTruthy();
  } finally {
    failure.mockRestore();
    random?.mockRestore();
    localStorage.removeItem(key);
  }
});

it('retains a new Stop when an initially unreadable older observation is later removed', async () => {
  const browserDatabase = '19191919-1919-4919-8919-191919191919';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const attempt = savedAttempt(stopId, 'stop', testId, true);
  localStorage.setItem(
    key,
    JSON.stringify([{ ...attempt, observationVersion: '20202020-2020-4020-8020-202020202020' }]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const originalGet = Storage.prototype.getItem;
  let unreadable = true;
  const read = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (
    this: Storage,
    itemKey,
  ) {
    if (this === localStorage && itemKey === key && unreadable) throw new Error('unavailable');
    return originalGet.call(this, itemKey);
  });
  const write = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  api.stop.mockRejectedValue(new Error('response lost'));
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  const props = {
    session: bridge,
    hostRunId: host,
    backendRunId: backend,
    databaseId: browserDatabase,
    pendingStorage: localStorage,
  };
  try {
    const mounted = render(<LiveApp {...props} />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    unreadable = false;
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    mounted.unmount();
    write.mockRestore();
    localStorage.setItem(key, '[]');
    render(<LiveApp {...props} />);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    const saved: { id: string; retired: boolean }[] = JSON.parse(localStorage.getItem(key) ?? '[]');
    expect(saved).toEqual(
      expect.arrayContaining([expect.objectContaining({ id: stopId, retired: false })]),
    );
    expect(api.stop).toHaveBeenCalledOnce();
  } finally {
    read.mockRestore();
    write.mockRestore();
    localStorage.removeItem(key);
  }
});

it('still sends Stop when the observation version cannot be generated', async () => {
  const browserDatabase = '18181818-1818-4818-8818-181818181818';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  localStorage.setItem(
    key,
    JSON.stringify([{ id: 'legacy-rng', kind: 'create', testId: null, retired: false }]),
  );
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.stop.mockRejectedValue(new Error('response lost'));
  const random = vi.spyOn(crypto, 'randomUUID').mockImplementation(() => {
    throw new Error('unavailable');
  });
  try {
    render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={browserDatabase}
        pendingStorage={localStorage}
      />,
    );
    const stop = await screen.findByRole('button', { name: 'Остановить испытание' });
    await userEvent.setup().click(stop);
    await userEvent.setup().click(stop);
    expect(api.stop).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('button', { name: 'Повторить чтение команд' })).toBeTruthy();
    expect(screen.getByText(new RegExp(`ID ${stopId}`))).toBeTruthy();
    expect(localStorage.getItem(key)).not.toContain(stopId);
    random.mockRestore();
    const failedWrite = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('unavailable');
    });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Я проверил эту команду' }));
    failedWrite.mockRestore();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить чтение команд' }));
    const saved: { id: string; observationVersion?: string }[] = JSON.parse(
      localStorage.getItem(key) ?? '[]',
    );
    expect(saved).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: stopId, observationVersion: expect.any(String) }),
      ]),
    );
    expect(api.stop).toHaveBeenCalledTimes(2);
  } finally {
    random.mockRestore();
    localStorage.removeItem(key);
  }
});

it('retains overflow until multiple requested removals can be saved, while Stop remains available', async () => {
  const browserDatabase = '40404040-4040-4040-8040-404040404040';
  const key = `gk80.demo.pending-command.v1:${browserDatabase}`;
  const secondTest = '50505050-5050-4050-8050-505050505050';
  const secondStop = '60606060-6060-4060-8060-606060606060';
  let active = { ...record, stage: TestRecordStage.Running };
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: active,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const props = {
    session: bridge,
    hostRunId: host,
    backendRunId: backend,
    databaseId: browserDatabase,
    pendingStorage: localStorage,
  };
  api.stop.mockRejectedValue(new Error('response lost'));
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  const failure = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable');
  });
  try {
    const first = render(<LiveApp {...props} />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    first.unmount();
    active = { ...active, testId: secondTest, stopCommandId: secondStop };
    const second = render(<LiveApp {...props} />);
    // The remount initially exposes last-known data; this attempt targets the next test.
    await waitFor(
      () =>
        expect(
          sessionObservation(host, backend).getSnapshotFor(bridge).lastSnapshot?.activeTest?.testId,
        ).toBe(secondTest),
      { timeout: 8000 },
    );
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    expect(api.stop).toHaveBeenLastCalledWith(secondTest, { commandId: secondStop });
    second.unmount();
    failure.mockRestore();
    const old = Array.from({ length: 65 }, (_, index) => ({
      id: `overflow-${index}`,
      kind: 'create',
      testId: null,
      retired: true,
      legacyReviewed: true,
    }));
    localStorage.setItem(key, JSON.stringify(old));
    render(<LiveApp {...props} />);
    await screen.findByText(/ID overflow-0\./);
    expect(screen.getByText(new RegExp(`ID ${stopId}`))).toBeTruthy();
    expect(screen.getByText(new RegExp(`ID ${secondStop}`))).toBeTruthy();
    await userEvent.setup().click(
      within(screen.getByText(/ID overflow-0\./)).getByRole('button', {
        name: 'Удалить запись наблюдения',
      }),
    );
    expect(screen.getByText(/ID overflow-0\./)).toBeTruthy();
    expect(JSON.parse(localStorage.getItem(key) ?? '[]')).toHaveLength(65);
    await userEvent.setup().click(
      within(screen.getByText(/ID overflow-1\./)).getByRole('button', {
        name: 'Удалить запись наблюдения',
      }),
    );
    expect(screen.queryByText(/ID overflow-0\./)).toBeNull();
    expect(screen.queryByText(/ID overflow-1\./)).toBeNull();
    const saved: { id: string }[] = JSON.parse(localStorage.getItem(key) ?? '[]');
    expect(saved).toHaveLength(65);
    expect(saved.map((item) => item.id)).toEqual(expect.arrayContaining([stopId, secondStop]));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Остановить испытание' }));
    expect(api.stop).toHaveBeenCalledTimes(3);
  } finally {
    failure.mockRestore();
    localStorage.removeItem(key);
  }
});

it('merges the previously unread command list before storing an emergency Stop', async () => {
  const oldId = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd';
  sessionStorage.setItem(
    pendingKey,
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
    measurementBatch: null,
    activeTest: { ...record, stage: TestRecordStage.Running },
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot,
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.command.mockReturnValue(new Promise(() => {}));
  api.stop.mockResolvedValue(
    stopResponse({
      commandId: stopId,
      status: CommandStatusStatus.CommandStatusStatusAccepted,
      testId,
    }),
  );
  render(
    <StrictMode>
      <LiveApp session={bridge} hostRunId={host} backendRunId={backend} />
    </StrictMode>,
  );
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  getItem.mockRestore();
  const saved: { id: string }[] = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
  expect(saved.map((item) => item.id)).toEqual(expect.arrayContaining([oldId, stopId]));
  expect(api.stop).toHaveBeenCalledTimes(1);
});

it('can retry reading an unavailable command list without sending a command', async () => {
  const oldId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee';
  sessionStorage.setItem(
    pendingKey,
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
    measurementBatch: null,
    activeTest: null,
    pendingAction: null,
    measurementTiming: null,
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
        leaseAcquireUnknown: false,
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
  expect(screen.getByText(/Команда:/)).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Новое испытание' }).hasAttribute('disabled')).toBe(
    true,
  );
  expect(api.create).not.toHaveBeenCalled();
});

it.each(['create', 'start', 'confirm', 'stop'] as const)(
  'normalizes a typed direct %s conflict through both adapters',
  async (kind) => {
    const response = { delivery: 'conflict', command: null };
    api[kind].mockResolvedValue(response);
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(response));
    const browser = createBrowserBridge(
      { generation: 'test', signal: new AbortController().signal },
      fetcher,
    ).service;
    const invoke = (service: typeof desktopDemoService) => {
      switch (kind) {
        case 'create':
          return service.createTest({
            commandId: 'id',
            expectedRevision: 0,
            parameters: record.parameters,
          });
        case 'start':
          return service.startTest(testId, {
            commandId: 'id',
            expectedRevision: 0,
            valveClosed: true,
            connectionConfirmed: true,
          });
        case 'confirm':
          return service.confirmAction(testId, actionId, { commandId: 'id', expectedRevision: 0 });
        case 'stop':
          return service.stopTest(testId, { commandId: 'id' });
      }
    };
    await expect(invoke(desktopDemoService)).rejects.toBeInstanceOf(CommandConflictError);
    await expect(invoke(browser)).rejects.toBeInstanceOf(CommandConflictError);
    expect(api[kind]).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
    const paths = {
      create: '/api/tests',
      start: `/api/tests/${testId}/start`,
      confirm: `/api/tests/${testId}/actions/${actionId}/confirm`,
      stop: `/api/tests/${testId}/stop`,
    };
    expect(fetcher.mock.calls[0]?.[0]).toBe(paths[kind]);
    const sent = api[kind].mock.calls[0]?.at(-1);
    expect(JSON.parse(String(fetcher.mock.calls[0]?.[1]?.body))).toEqual(sent);
    const expectedArgs =
      kind === 'create' ? [sent] : kind === 'confirm' ? [testId, actionId, sent] : [testId, sent];
    expect(api[kind]).toHaveBeenCalledWith(...expectedArgs);

    for (const value of [
      { delivery: 'notSent', command: null },
      {
        delivery: 'response',
        command: { commandId: '10000000-0000-4000-8000-000000000001', status: 'completed' },
      },
      { delivery: 'conflict' },
      { delivery: 'conflict', command: null, extra: true },
      { delivery: 'conflict', command: {} },
    ]) {
      api[kind].mockResolvedValueOnce(value);
      fetcher.mockResolvedValueOnce(Response.json(value));
      await expect(invoke(desktopDemoService)).resolves.toEqual(value);
      if (value.delivery === 'notSent' || value.delivery === 'response')
        await expect(invoke(browser)).resolves.toEqual(value);
      else {
        const error: unknown = await invoke(browser).catch((failure: unknown) => failure);
        expect(error).toBeInstanceOf(Error);
        expect(error).not.toBeInstanceOf(CommandConflictError);
      }
    }
    api[kind].mockRejectedValueOnce(new Error('response lost'));
    fetcher.mockRejectedValueOnce(new Error('response lost'));
    for (const service of [desktopDemoService, browser]) {
      const error: unknown = await invoke(service).catch((failure: unknown) => failure);
      expect(error).toBeInstanceOf(Error);
      expect(error).not.toBeInstanceOf(CommandConflictError);
    }
    expect(api[kind]).toHaveBeenCalledTimes(7);
    expect(fetcher).toHaveBeenCalledTimes(7);
  },
);

it.each([false, true])(
  'retains only a previous unknown Stop after notSent (previous=%s)',
  async (previous) => {
    if (previous)
      sessionStorage.setItem(
        pendingKey,
        JSON.stringify([savedAttempt(stopId, 'stop', testId, false)]),
      );
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 2,
          lastEventSequence: 2,
          measurementBatch: null,
          activeTest: { ...record, stage: TestRecordStage.Running },
          pendingAction: null,
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
    api.stop.mockResolvedValue({ delivery: 'notSent', command: null });
    render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    await screen.findByText(/Новый запрос остановки не отправлен/);
    const saved: unknown[] = JSON.parse(sessionStorage.getItem(pendingKey) ?? '[]');
    expect(saved).toHaveLength(previous ? 1 : 0);
    expect(api.stop).toHaveBeenCalledOnce();
  },
);

it('does not accept a raw Stop status as a dispatch response', async () => {
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.stop.mockResolvedValue({ commandId: stopId, testId, status: 'completed' });
  render(<LiveApp session={bridge} hostRunId={host} backendRunId={backend} />);
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
  await screen.findByText(/Исход остановки неизвестен/);
  expect(screen.queryByText('Остановка подтверждена ядром.')).toBeNull();
});

it.each([
  ['wails', 'start'],
  ['browser', 'start'],
  ['wails', 'confirm'],
  ['browser', 'confirm'],
  ['wails', 'stop'],
  ['browser', 'stop'],
] as const)(
  'reports a direct %s %s conflict from the adapter in React',
  async (adapter, kind) => {
    const bridge: SessionBridge = {
      beginSessionCheck: async () => 'ticket',
      keepAlive: async () => {},
      sessionView: async () => ({
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
          revision: 2,
          lastEventSequence: 2,
          measurementBatch: null,
          activeTest: {
            ...record,
            stage: kind === 'start' ? TestRecordStage.Preparing : TestRecordStage.Running,
          },
          pendingAction:
            kind === 'confirm' ? { testId, actionId, instruction: 'Подтвердите измерение' } : null,
          measurementTiming: null,
          measurements: [],
        },
        validForMs: 6000,
        linkValidForMs: 6000,
      }),
    };
    const conflict = { delivery: 'conflict', command: null };
    api[kind].mockResolvedValue(conflict);
    api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
    api.readiness.mockResolvedValue({ testId, revision: 2, ready: true, checks: [] });
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(conflict));
    const browser = createBrowserBridge(
      { generation: 'test', signal: new AbortController().signal },
      fetcher,
    ).service;
    const service =
      adapter === 'wails'
        ? desktopDemoService
        : {
            ...desktopDemoService,
            ...(kind === 'start'
              ? { startTest: browser.startTest }
              : kind === 'confirm'
                ? { confirmAction: browser.confirmAction }
                : { stopTest: browser.stopTest }),
          };
    render(<LiveApp session={bridge} service={service} hostRunId={host} backendRunId={backend} />);
    const user = userEvent.setup();
    if (kind === 'start') {
      await screen.findByRole('heading', { name: 'Автоматические проверки' });
      await user.click(screen.getByLabelText(/Клапан закрыт/));
      await user.click(screen.getByLabelText(/Подключение соответствует направлению/));
      const start = screen.getByRole('button', { name: /Перейти к проведению/ });
      await waitFor(() => expect(start.hasAttribute('disabled')).toBe(false));
      await user.click(start);
    } else {
      const button = await screen.findByRole('button', {
        name: kind === 'stop' ? 'Остановить испытание' : 'Подтвердить действие',
      });
      await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false));
      await user.click(button);
    }
    await screen.findByText(
      kind === 'stop' ? /Запрос остановки отклонён/ : /Идентификатор принадлежит другой команде/,
    );
    expect(screen.queryByText('Остановка подтверждена ядром.')).toBeNull();
    if (adapter === 'browser') expect(fetcher).toHaveBeenCalledOnce();
    else expect(api[kind]).toHaveBeenCalledOnce();
  },
  10000,
);

it('retains an unread earlier Stop after Storage recovers and a new request is not sent', async () => {
  const db = '77777777-7777-4777-8777-777777777777';
  const key = `gk80.demo.pending-command.v1:${db}`;
  const previous = savedAttempt(stopId, 'stop', testId, false);
  localStorage.setItem(key, JSON.stringify([previous]));
  const originalGet = Storage.prototype.getItem;
  let failedReads = 0;
  const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (
    this: Storage,
    name: string,
  ) {
    if (this === localStorage && name === key && failedReads === 0) {
      failedReads++;
      throw new Error('unavailable');
    }
    return originalGet.call(this, name);
  });
  const bridge: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
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
        revision: 2,
        lastEventSequence: 2,
        measurementBatch: null,
        activeTest: { ...record, stage: TestRecordStage.Running },
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  api.exact.mockResolvedValue({ outcome: 'unknown', command: null });
  api.stop.mockResolvedValue({ delivery: 'notSent', command: null });
  try {
    render(
      <LiveApp
        session={bridge}
        hostRunId={host}
        backendRunId={backend}
        databaseId={db}
        pendingStorage={localStorage}
      />,
    );
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Остановить испытание' }));
    await screen.findByText(/Новый запрос остановки не отправлен/);
    expect(failedReads).toBe(1);
    const stored: unknown[] = JSON.parse(originalGet.call(localStorage, key) ?? '[]');
    expect(stored).toContainEqual(
      expect.objectContaining({ id: stopId, kind: 'stop', originalJSON: previous.originalJSON }),
    );
    await waitFor(() =>
      expect(api.exact).toHaveBeenCalledWith(db, 'stop', testId, '', stopId, previous.originalJSON),
    );
    expect(api.stop).toHaveBeenCalledOnce();
  } finally {
    getItem.mockRestore();
    localStorage.removeItem(key);
  }
});
