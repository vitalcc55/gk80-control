import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';

vi.mock('../src/components/MeasurementChart', () => ({ MeasurementChart: () => null }));

import type { SessionBridge } from '../src/desktop/bridge';
import { History } from '../src/desktop/History';
import { LiveApp } from '../src/desktop/LiveApp';
import { desktopDemoService } from '../src/desktop/wails-bridge';
import {
  ConnectionStateStatus,
  type TestList,
  TestParametersDirection,
  type TestRecord,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

const record: TestRecord = {
  testId: '33333333-3333-4333-8333-333333333333',
  stopCommandId: '55555555-5555-4555-8555-555555555555',
  createdAt: '2026-11-05T00:00:00Z',
  operatorId: 'demo',
  demo: true,
  stage: TestRecordStage.Finished,
  outcome: TestRecordOutcome.TestRecordOutcomeCompleted,
  saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
  dataQuality: TestRecordDataQuality.TestRecordDataQualityComplete,
  parameters: {
    valve: 'Первая запись',
    areaM2: 0.012,
    pmaxPa: 10000,
    measurementSeconds: 30,
    direction: TestParametersDirection.TestParametersDirectionPressure,
    actuator: false,
  },
};
const callbacks = { onNew: vi.fn(), onCopy: vi.fn(), onDiscard: vi.fn() };

it.each(['navigation', 'connection'])(
  'keeps an invalid applied range rejected across %s and recovers on reset',
  async (route) => {
    const listTests = vi.fn().mockResolvedValue({ items: [record], nextAfter: record.testId });
    const service = { ...desktopDemoService, listTests };
    const draw = (enabled: boolean) => (
      <History
        {...callbacks}
        service={service}
        available={route === 'connection' ? enabled : true}
        visible={route === 'navigation' ? enabled : true}
        disabled={false}
        draftAvailable={false}
      />
    );
    const view = render(draw(true));
    const user = userEvent.setup();
    await screen.findByRole('button', { name: /Открыть Первая/ });
    await user.type(screen.getByLabelText('С даты'), '2026-11-10');
    await user.type(screen.getByLabelText('По дату'), '2026-11-05');
    await user.click(screen.getByRole('button', { name: 'Найти' }));
    expect(screen.getByText('Начало диапазона должно быть не позже конца.')).toBeTruthy();
    const calls = listTests.mock.calls.length;
    view.rerender(draw(false));
    view.rerender(draw(true));
    expect(screen.getByText('Начало диапазона должно быть не позже конца.')).toBeTruthy();
    expect(listTests).toHaveBeenCalledTimes(calls);
    expect(screen.queryByRole('button', { name: /Открыть Первая/ })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Сбросить поиск' }));
    expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
    expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: null });
  },
);

it('returns from a copied draft to the list instead of resurrecting the saved result', async () => {
  const host = '11111111-1111-4111-8111-111111111111';
  const backend = '22222222-2222-4222-8222-222222222222';
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
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockResolvedValue(record),
  };
  render(
    <LiveApp
      session={session}
      service={service}
      hostRunId={host}
      backendRunId={backend}
      databaseId="77777777-7777-4777-8777-777777777777"
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  await user.click(await screen.findByRole('button', { name: 'Новое по этим параметрам' }));
  expect(screen.getByRole('main', { name: 'Параметры' })).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'К испытаниям' }));
  expect(screen.getByRole('main', { name: 'Испытания' })).toBe(document.activeElement);
  expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'Результат испытания' })).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Продолжить подготовку' }));
  expect(screen.getByLabelText('Обозначение или наименование клапана')).toHaveProperty(
    'value',
    record.parameters.valve,
  );
  expect(service.test).toHaveBeenCalledOnce();
});

it('queries the whole source with calendar/outcome filters, resets the cursor and rejects a late old page', async () => {
  let finish: (value: TestList) => void = () => {};
  const oldPage = new Promise<TestList>((resolve) => {
    finish = resolve;
  });
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockReturnValueOnce(oldPage)
    .mockResolvedValueOnce({
      items: [
        {
          ...record,
          testId: '66666666-6666-4666-8666-666666666666',
          parameters: { ...record.parameters, valve: 'За первой страницей' },
        },
      ],
      nextAfter: null,
    });
  render(
    <History
      {...callbacks}
      service={{ ...desktopDemoService, listTests }}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: record.testId });
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'За первой');
  await user.selectOptions(screen.getByLabelText('Исход'), 'completed');
  await user.type(screen.getByLabelText('С даты'), '2026-11-05');
  await user.type(screen.getByLabelText('По дату'), '2026-11-05');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(await screen.findByRole('button', { name: /Открыть За первой страницей/ })).toBeTruthy();
  const start = new Date('2026-11-05T00:00:00');
  const end = new Date('2026-11-06T00:00:00');
  expect(listTests).toHaveBeenLastCalledWith({
    limit: 100,
    after: null,
    q: 'За первой',
    outcome: 'completed',
    createdFrom: start.toISOString(),
    createdBefore: end.toISOString(),
  });
  await act(async () => {
    finish({ items: [record], nextAfter: record.testId });
    await oldPage;
  });
  expect(screen.queryByRole('button', { name: /Открыть Первая запись/ })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Следующая страница' })).toBeNull();
});

it('keeps filters and the current bounded page across hidden navigation and ignores a late record open', async () => {
  let finish: (value: TestRecord) => void = () => {};
  const pending = new Promise<TestRecord>((resolve) => {
    finish = resolve;
  });
  const listTests = vi.fn().mockResolvedValue({ items: [record], nextAfter: null });
  const service = { ...desktopDemoService, listTests, test: vi.fn().mockReturnValue(pending) };
  const onViewChange = vi.fn();
  const draw = (visible: boolean) => (
    <History
      {...callbacks}
      onViewChange={onViewChange}
      visible={visible}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true));
  const user = userEvent.setup();
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Первая');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(draw(false));
  await act(async () => {
    finish(record);
    await pending;
  });
  expect(onViewChange).not.toHaveBeenCalled();
  const calls = listTests.mock.calls.length;
  view.rerender(draw(true));
  await waitFor(() => expect(listTests).toHaveBeenCalledTimes(calls + 1));
  expect(screen.getByLabelText('Поиск по номеру или изделию')).toHaveProperty('value', 'Первая');
  expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: null, q: 'Первая' });
});

it('opens the shared result before reading details and retains the record when a detail page fails', async () => {
  const measurements = vi
    .fn()
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue({ items: [], nextAfter: null });
  const audit = vi.fn().mockResolvedValue({ items: [], nextAfter: null });
  const onCopy = vi.fn();
  render(
    <History
      {...callbacks}
      onCopy={onCopy}
      service={{
        ...desktopDemoService,
        listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
        test: vi.fn().mockResolvedValue(record),
        measurements,
        audit,
      }}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  expect(await screen.findByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  expect(screen.getByText('Ход испытания')).toBeTruthy();
  expect(screen.getByText('Данные')).toBeTruthy();
  expect(screen.getByText('Сохранение')).toBeTruthy();
  expect(measurements).not.toHaveBeenCalled();
  expect(audit).not.toHaveBeenCalled();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  expect(await screen.findByText('Не удалось прочитать измерения.')).toBeTruthy();
  expect(screen.getByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  expect(await screen.findByText('Измерений нет.')).toBeTruthy();
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
  await user.click(screen.getByRole('button', { name: 'Новое по этим параметрам' }));
  expect(onCopy).toHaveBeenCalledWith(record.parameters);
});

it('starts a changed offline query at the first cursor on reconnect, including unknown outcomes', async () => {
  const listTests = vi.fn().mockResolvedValue({ items: [record], nextAfter: record.testId });
  const service = { ...desktopDemoService, listTests };
  const draw = (available: boolean) => (
    <History
      {...callbacks}
      service={service}
      available={available}
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true));
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  await screen.findByRole('button', { name: /Открыть Первая/ });
  view.rerender(draw(false));
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Другая выборка');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  view.rerender(draw(true));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: null, q: 'Другая выборка' }),
  );
  await user.selectOptions(screen.getByLabelText('Исход'), 'unknown');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({
      limit: 100,
      after: null,
      q: 'Другая выборка',
      outcome: 'unknown',
    }),
  );
});

it('cancels pending record opening when navigation explicitly returns to the list', async () => {
  let finish: (value: TestRecord) => void = () => {};
  const pending = new Promise<TestRecord>((resolve) => {
    finish = resolve;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockReturnValue(pending),
  };
  const onViewChange = vi.fn();
  const draw = (resetVersion: number) => (
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      resetVersion={resetVersion}
      onViewChange={onViewChange}
    />
  );
  const view = render(draw(0));
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(draw(1));
  expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
  await act(async () => {
    finish(record);
    await pending;
  });
  expect(onViewChange).not.toHaveBeenCalled();
});
