import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { type ComponentProps, StrictMode, useState } from 'react';
import { expect, it, vi } from 'vitest';
import { AppShell } from '../src/components/AppShell';
import { StopButton } from '../src/components/StopButton';
import { useScreenFocus } from '../src/components/use-screen-focus';
import { LiveRun as LiveRunView } from '../src/desktop/LiveRun';
import { MeasurementSeriesContext } from '../src/desktop/measurement-series';
import { useMeasurementObservation } from '../src/desktop/use-measurement-series';
import { formatMeasurement } from '../src/lib/format';

function LiveRun({
  gapVersion = 0,
  ...props
}: Omit<ComponentProps<typeof LiveRunView>, 'measurementContext' | 'measurementKey'> & {
  gapVersion?: number;
}) {
  const [context] = useState(() => new MeasurementSeriesContext());
  useMeasurementObservation(context, props.record.testId, props.snapshot, props.fresh, gapVersion);
  return (
    <LiveRunView {...props} measurementContext={context} measurementKey={props.record.testId} />
  );
}

import {
  MeasurementChannel,
  MeasurementQuality,
  MeasurementQuantity,
  MeasurementSource,
  MeasurementUnit,
  type Snapshot,
  TestParametersDirection,
  type TestRecord,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { GraphPoint } from '../src/model';

vi.mock('../src/components/MeasurementChart', () => ({
  MeasurementChart: ({ points }: { points: readonly GraphPoint[] }) => (
    <div data-testid="chart" data-points={JSON.stringify(points)}>
      {points.map((point) => point.pressure ?? 'gap').join(',')}
    </div>
  ),
}));

const record: TestRecord = {
  testId: '33333333-3333-4333-8333-333333333333',
  stopCommandId: '55555555-5555-4555-8555-555555555555',
  createdAt: '2026-10-30T00:00:00Z',
  operatorId: 'demo',
  demo: true,
  stage: TestRecordStage.Running,
  outcome: TestRecordOutcome.TestRecordOutcomePending,
  dataQuality: TestRecordDataQuality.TestRecordDataQualityPending,
  saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending,
  parameters: {
    valve: 'К-1',
    areaM2: 0.012,
    pmaxPa: 10000,
    measurementSeconds: 60,
    preOpenStabilizationSeconds: 10,
    postCloseStabilizationSeconds: 10,
    direction: TestParametersDirection.TestParametersDirectionPressure,
    actuator: false,
  },
};
const first = {
  channel: MeasurementChannel.Dd1,
  measuredAt: '2026-10-30T00:00:01Z',
  observedAt: '2026-10-30T00:00:01Z',
  quality: MeasurementQuality.MeasurementQualityValid,
  quantity: MeasurementQuantity.MeasurementQuantityPressure,
  sequence: 1,
  source: MeasurementSource.MeasurementSourceSimulator,
  unit: MeasurementUnit.Pa,
  value: 1000,
};
const snapshot: Snapshot = {
  apiVersion: 'v1',
  backendRunId: '22222222-2222-4222-8222-222222222222',
  revision: 1,
  lastEventSequence: 1,
  activeTest: record,
  pendingAction: null,
  measurementBatch: 1,
  measurementTiming: null,
  measurements: [first],
};

it('округляет показания и различает малые значения обоих знаков и ноль', () => {
  expect([0, -0, 0.0001, -0.0001, 0.001, -0.001, 1.2344, 1.2346].map(formatMeasurement)).toEqual([
    '≈ 0 Па',
    '≈ 0 Па',
    '< 0,001 Па',
    '−0,001 < значение < 0 Па',
    '≈ 0,001 Па',
    '≈ -0,001 Па',
    '≈ 1,234 Па',
    '≈ 1,235 Па',
  ]);
  expect(formatMeasurement(1e200)).toMatch(/E\+?200/i);
  expect(formatMeasurement(1e200).length).toBeLessThan(40);
  expect(formatMeasurement(1000000000000.1234)).toBe('≈ 1,000000000000123E12 Па');
  expect(formatMeasurement(-1000000000000.1234)).toBe('≈ -1,000000000000123E12 Па');
  expect(formatMeasurement(Number.MAX_VALUE)).not.toMatch(/Infinity|∞/);
  expect(formatMeasurement(-Number.MAX_VALUE)).not.toMatch(/Infinity|∞/);
});

it('показывает точные границы малых показаний, сохраняя исходные точки', async () => {
  const measured = {
    ...snapshot,
    measurements: [
      { ...first, value: 0.0001 },
      { ...first, channel: MeasurementChannel.Dd2, value: -0.0001 },
      { ...first, channel: MeasurementChannel.Dd3, value: 1.23456 },
    ],
  };
  render(
    <LiveRun record={record} snapshot={measured} fresh pending={false} onConfirm={() => {}} />,
  );
  expect(screen.getByText('< 0,001 Па')).toBeTruthy();
  expect(screen.getByText('−0,001 < значение < 0 Па')).toBeTruthy();
  await userEvent.setup().click(screen.getByText('Время и качество показаний'));
  expect(await screen.findByText(/ДД3: ≈ 1,235 Па/)).toBeTruthy();
  expect(screen.getByTestId('chart').getAttribute('data-points')).toContain('0.0001');
  expect(screen.queryByText(/округление до 6/)).toBeNull();
});

it('называет запуском только известное подтверждение начала измерения', () => {
  const confirm = vi.fn();
  const action = {
    testId: record.testId,
    actionId: 'action',
    instruction: 'Подтвердите начало демонстрационного измерения',
  };
  const draw = (pendingAction: typeof action, fresh = true) => (
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, pendingAction }}
      fresh={fresh}
      pending={false}
      onConfirm={confirm}
    />
  );
  const view = render(draw(action));
  fireEvent.click(screen.getByRole('button', { name: 'Запуск испытания' }));
  expect(confirm).toHaveBeenCalledOnce();
  expect(screen.queryByText(action.instruction)).toBeNull();
  view.rerender(draw(action, false));
  expect(screen.getByRole('button', { name: 'Запуск испытания' }).hasAttribute('disabled')).toBe(
    true,
  );
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, pendingAction: action }}
      fresh
      pending
      onConfirm={confirm}
    />,
  );
  expect(screen.getByRole('button', { name: 'Запуск испытания' }).hasAttribute('disabled')).toBe(
    true,
  );
  view.rerender(
    <LiveRun
      record={{ ...record, stage: TestRecordStage.Stopping }}
      snapshot={{ ...snapshot, pendingAction: action }}
      fresh
      pending={false}
      onConfirm={confirm}
    />,
  );
  expect(screen.queryByRole('button', { name: 'Запуск испытания' })).toBeNull();
  view.rerender(draw({ ...action, instruction: 'Проверьте подключение' }));
  expect(screen.getByText('Проверьте подключение')).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Подтвердить действие' })).toBeTruthy();
});

it('leaves a gap when an accepted measurement packet was not observed', () => {
  const context = new MeasurementSeriesContext();
  context.accept('test', { ...snapshot, measurementBatch: 1 }, true, 0);
  context.accept(
    'test',
    {
      ...snapshot,
      measurementBatch: 3,
      measurements: [{ ...first, sequence: 7, measuredAt: '2026-10-30T00:00:03Z' }],
    },
    true,
    0,
  );
  expect(context.read('test').points.map((point) => point.pressure)).toEqual([1000, null, 1000]);
});

it('keeps adjacent packets connected and ignores repeated or older packets', () => {
  const context = new MeasurementSeriesContext();
  const next = {
    ...snapshot,
    measurementBatch: 2,
    measurements: [{ ...first, sequence: 4, measuredAt: '2026-10-30T00:00:02Z', value: 1001 }],
  };
  context.accept('test', snapshot, true, 0);
  context.accept('test', next, true, 0);
  context.accept('test', next, true, 0);
  context.accept('test', snapshot, true, 0);
  expect(context.read('test').points.map((point) => point.pressure)).toEqual([1000, 1001]);
});

function FocusShell({ children }: { children: ReactNode }) {
  return (
    <AppShell
      screen="tests"
      active={false}
      draftAvailable={false}
      onTests={() => {}}
      onRun={() => {}}
      onParameters={() => {}}
    >
      {children}
    </AppShell>
  );
}

function DelayedNavigation() {
  const [screenName, setScreenName] = useState<'tests' | 'run'>('tests');
  const { mainRef, requestFocus } = useScreenFocus(screenName);
  return (
    <>
      <button type="button" onClick={() => requestFocus('run')}>
        Начать
      </button>
      <button type="button">Другая навигация</button>
      <button type="button" onClick={() => setScreenName('run')}>
        Ответ ядра
      </button>
      <main ref={mainRef} tabIndex={-1} />
    </>
  );
}

it('does not steal focus after the operator moves while waiting for a screen', () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    render(<DelayedNavigation />);
    fireEvent.click(screen.getByRole('button', { name: 'Начать' }));
    const other = screen.getByRole('button', { name: 'Другая навигация' });
    other.focus();
    fireEvent.click(screen.getByRole('button', { name: 'Ответ ядра' }));
    expect(document.activeElement).toBe(other);
  } finally {
    hasFocus.mockRestore();
  }
});

it('focuses an explicitly requested screen unless the window loses focus', () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const view = render(<DelayedNavigation />);
    fireEvent.click(screen.getByRole('button', { name: 'Начать' }));
    fireEvent.click(screen.getByRole('button', { name: 'Ответ ядра' }));
    expect(document.activeElement).toBe(screen.getByRole('main'));
    view.unmount();
    render(<DelayedNavigation />);
    screen.getByRole('button', { name: 'Начать' }).focus();
    fireEvent.click(screen.getByRole('button', { name: 'Начать' }));
    fireEvent.blur(window);
    fireEvent.click(screen.getByRole('button', { name: 'Ответ ядра' }));
    expect(document.activeElement).not.toBe(screen.getByRole('main'));
  } finally {
    hasFocus.mockRestore();
  }
});

it('keeps keyboard focus in the current screen when its action disappears', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const waiting = {
      ...snapshot,
      pendingAction: { testId: record.testId, actionId: 'action', instruction: 'Подтвердите' },
    };
    const draw = (value: Snapshot) => (
      <FocusShell>
        <LiveRun record={record} snapshot={value} fresh pending={false} onConfirm={() => {}} />
      </FocusShell>
    );
    const view = render(draw(waiting));
    screen.getByRole('button', { name: 'Подтвердить действие' }).focus();
    view.rerender(draw(snapshot));
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('returns focus after a focused stop is removed and leaves a different control alone', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const draw = (showRun: boolean) => (
      <FocusShell>
        <button type="button">Другой элемент</button>
        {showRun && (
          <LiveRun
            record={record}
            snapshot={snapshot}
            fresh
            pending={false}
            onConfirm={() => {}}
            stop={<StopButton disabled={false} onStop={() => {}} />}
          />
        )}
      </FocusShell>
    );
    const view = render(draw(true));
    screen.getByRole('button', { name: 'Остановить испытание' }).focus();
    view.rerender(draw(false));
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
    view.rerender(draw(true));
    screen.getByRole('button', { name: 'Остановить испытание' }).focus();
    view.rerender(draw(false));
    screen.getByRole('button', { name: 'Другой элемент' }).focus();
    await Promise.resolve();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Другой элемент' }));
  } finally {
    hasFocus.mockRestore();
  }
});

it('recovers focus if the focused stop becomes disabled', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const draw = (disabled: boolean) => (
      <FocusShell>
        <StopButton disabled={disabled} onStop={() => {}} />
      </FocusShell>
    );
    const view = render(draw(false));
    screen.getByRole('button', { name: 'Остановить испытание' }).focus();
    view.rerender(draw(true));
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('tracks a repeated pointer press on the focused control in StrictMode', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const draw = (show: boolean) => (
      <StrictMode>
        <FocusShell>
          {show && (
            <button type="button">
              <span>Действие</span>
            </button>
          )}
        </FocusShell>
      </StrictMode>
    );
    const view = render(draw(true));
    screen.getByRole('button', { name: 'Действие' }).focus();
    fireEvent.pointerDown(screen.getByText('Действие'));
    view.rerender(draw(false));
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('does not restore focus after a pointer press elsewhere', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const draw = (show: boolean) => (
      <FocusShell>
        <div>Свободная область</div>
        {show && <StopButton disabled={false} onStop={() => {}} />}
      </FocusShell>
    );
    const view = render(draw(true));
    screen.getByRole('button', { name: 'Остановить испытание' }).focus();
    fireEvent.pointerDown(screen.getByText('Свободная область'));
    view.rerender(draw(false));
    await Promise.resolve();
    expect(document.activeElement).toBe(document.body);
  } finally {
    hasFocus.mockRestore();
  }
});

it('does not restore focus into a document that is not focused', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(false);
  try {
    const draw = (show: boolean) => (
      <FocusShell>{show && <StopButton disabled={false} onStop={() => {}} />}</FocusShell>
    );
    const view = render(draw(true));
    screen.getByRole('button', { name: 'Остановить испытание' }).focus();
    view.rerender(draw(false));
    await Promise.resolve();
    expect(document.activeElement).toBe(document.body);
  } finally {
    hasFocus.mockRestore();
  }
});

it('does not claim that a stopping stage means the stop operation is still running', () => {
  const stopping = { ...record, stage: TestRecordStage.Stopping };
  render(
    <LiveRun
      record={stopping}
      snapshot={{ ...snapshot, activeTest: stopping }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.queryByText(/остановка выполняется/i)).toBeNull();
  expect(screen.getByText(/безопасное давление не подтверждено/i)).toBeTruthy();
});

it('does not claim measurement progress before the first accepted observation', () => {
  const draw = (value: Snapshot) => (
    <LiveRun record={record} snapshot={value} fresh pending={false} onConfirm={() => {}} />
  );
  const view = render(
    draw({ ...snapshot, measurements: [], measurementBatch: null, measurementTiming: null }),
  );
  expect(screen.getByRole('heading', { name: 'Ожидание данных измерения' })).toBeTruthy();
  expect(screen.getByText('Ожидаем данные измерения ядра.')).toBeTruthy();
  expect(screen.getByText('Измерение').closest('li')?.getAttribute('aria-current')).toBeNull();
  view.rerender(
    draw({ ...snapshot, measurementTiming: { elapsedSeconds: 0, remainingSeconds: 60 } }),
  );
  expect(screen.getByRole('heading', { name: 'Измерение' })).toBeTruthy();
  expect(
    screen
      .getByText('Измерение', { selector: 'strong' })
      .closest('li')
      ?.getAttribute('aria-current'),
  ).toBe('step');
});

it('shows channel staleness without opening details and identifies the time observation', () => {
  render(
    <LiveRun
      record={record}
      snapshot={{
        ...snapshot,
        measurementTiming: { elapsedSeconds: 5.98, remainingSeconds: 54.02 },
        measurements: [
          first,
          {
            ...first,
            channel: MeasurementChannel.Dd2,
            value: 150,
            quality: MeasurementQuality.MeasurementQualityStale,
          },
        ],
      }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  const reading = screen.getByText('Перепад на диафрагме').closest('.reading');
  if (!(reading instanceof HTMLElement)) throw new Error('Differential reading missing');
  expect(within(reading).getByText('устарело · последний отсчёт')).toBeTruthy();
  expect(screen.queryByText('Время на последнем принятом отсчёте ядра')).toBeNull();
  expect(screen.getByText('5 с')).toBeTruthy();
  expect(screen.getByText('55 с')).toBeTruthy();
});

it('does not invent completed measurement or pressure reduction after an early Stop', () => {
  render(
    <LiveRun
      record={{ ...record, stage: TestRecordStage.Stopping }}
      snapshot={{ ...snapshot, measurements: [], measurementBatch: null, measurementTiming: null }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  const stages = screen.getByRole('complementary', { name: 'Этапы испытания' });
  expect(within(stages).queryByText('Предыдущий этап')).toBeNull();
  expect(within(stages).queryByText('Снижение давления')).toBeNull();
  expect(within(stages).getByText('Остановка')).toBeTruthy();
  expect(screen.getByText(/безопасное давление не подтверждено/)).toBeTruthy();
});

it('retains isolated recent source contexts and evicts the least recently observed context', () => {
  const context = new MeasurementSeriesContext();
  context.accept('source-A/database-A/test-A', snapshot, true, 0);
  context.markGap('source-A/database-A/test-A');
  expect(context.read('source-B/database-B/test-A').points).toEqual([]);
  context.accept(
    'source-B/database-B/test-A',
    { ...snapshot, measurements: [{ ...first, value: 2000 }] },
    true,
    0,
  );
  context.accept(
    'source-A/database-A/test-A',
    {
      ...snapshot,
      measurementBatch: 2,
      measurementTiming: null,
      measurements: [{ ...first, sequence: 2, measuredAt: '2026-10-30T00:00:03Z', value: 1002 }],
    },
    true,
    0,
  );
  expect(context.read('source-A/database-A/test-A').points).toEqual([
    { seconds: 0, pressure: 1000, differential: null },
    { seconds: 1, pressure: null, differential: null },
    { seconds: 2, pressure: 1002, differential: null },
  ]);
  for (const key of ['source-C', 'source-D', 'source-E']) context.accept(key, snapshot, true, 0);
  expect(context.read('source-B/database-B/test-A').points).toEqual([]);
  expect(context.read('source-A/database-A/test-A').points).toHaveLength(3);
});

it('isolates a new test and bounds retained points without changing the original time axis', () => {
  const view = render(
    <LiveRun record={record} snapshot={snapshot} fresh pending={false} onConfirm={() => {}} />,
  );
  for (let index = 1; index <= 300; index++) {
    view.rerender(
      <LiveRun
        record={record}
        snapshot={{
          ...snapshot,
          measurementBatch: index + 1,
          measurementTiming: null,
          measurements: [
            {
              ...first,
              sequence: index + 1,
              value: 1000 + index,
              measuredAt: new Date(Date.parse(first.measuredAt) + index * 1000).toISOString(),
            },
          ],
        }}
        fresh
        pending={false}
        onConfirm={() => {}}
      />,
    );
  }
  const chart = screen.getByTestId('chart');
  const retained: GraphPoint[] = JSON.parse(chart.getAttribute('data-points') ?? '[]');
  expect(retained).toHaveLength(300);
  expect(retained[0]).toEqual({ seconds: 1, pressure: 1001, differential: null });
  expect(retained[299]).toEqual({ seconds: 300, pressure: 1300, differential: null });
  const other = { ...record, testId: '88888888-8888-4888-8888-888888888888' };
  view.rerender(
    <LiveRun
      record={other}
      snapshot={{ ...snapshot, activeTest: other }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(chart.getAttribute('data-points')).toBe(
    JSON.stringify([{ seconds: 0, pressure: 1000, differential: null }]),
  );
});

it('keeps the last received reading visible while a new snapshot is being confirmed', async () => {
  const withAction = {
    ...snapshot,
    measurementTiming: null,
    measurements: [
      first,
      { ...first, channel: MeasurementChannel.Dd2, value: 150 },
      { ...first, channel: MeasurementChannel.Dd3, value: 80 },
    ],
    pendingAction: {
      testId: record.testId,
      actionId: '44444444-4444-4444-8444-444444444444',
      instruction: 'Подтвердите',
    },
  };
  const renderRun = (fresh: boolean) => (
    <LiveRun
      record={record}
      snapshot={withAction}
      fresh={fresh}
      pending={false}
      onConfirm={() => {}}
    />
  );
  const view = render(renderRun(true));
  expect(screen.getAllByText(/≈ 1\s?000 Па/)[0]).toBeTruthy();
  expect(
    screen.getByRole('button', { name: 'Подтвердить действие' }).hasAttribute('disabled'),
  ).toBe(false);
  view.rerender(renderRun(false));
  expect(screen.queryByText('По последнему полученному снимку')).toBeNull();
  expect(screen.getByText('Подтвердите')).toBeTruthy();
  expect(screen.getAllByText(/≈ 1\s?000 Па/)[0]).toBeTruthy();
  expect(screen.getByText('≈ 150 Па')).toBeTruthy();
  expect(screen.queryByText(/ДД3:.*≈ 80 Па/)).toBeNull();
  await userEvent.setup().click(screen.getByText('Время и качество показаний'));
  expect(await screen.findByText(/ДД3:.*≈ 80 Па/)).toBeTruthy();
  expect(screen.queryByText(/Последние полученные показания/)).toBeNull();
  expect(screen.getAllByText(/качество последнего наблюдения: достоверно/)).toHaveLength(3);
  expect(screen.getAllByText(/2026-10-30T00:00:01Z/)).toHaveLength(3);
  expect(screen.queryByText('Этап по последнему полученному снимку')).toBeNull();
  expect(
    screen.getByRole('button', { name: 'Подтвердить действие' }).hasAttribute('disabled'),
  ).toBe(true);
  expect(screen.getByTestId('chart').textContent).toBe('1000');
  const next = { ...first, sequence: 2, measuredAt: '2026-10-30T00:00:02Z', value: 1001 };
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, measurementBatch: next.sequence, measurements: [next] }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.getAllByText(/≈ 1\s?001 Па/)[0]).toBeTruthy();
  expect(screen.getByTestId('chart').textContent).toBe('1000,1001');
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{
        ...snapshot,
        measurementBatch: 3,
        measurementTiming: null,
        measurements: [
          {
            ...first,
            sequence: 3,
            value: null,
            measuredAt: null,
            quality: MeasurementQuality.MeasurementQualityUnavailable,
          },
        ],
      }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.queryByText(/≈ 1\s?001 Па/)).toBeNull();
  expect(screen.getByText(/качество последнего наблюдения: недоступно/)).toBeTruthy();
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{
        ...snapshot,
        measurementTiming: null,
        measurements: [
          {
            ...first,
            sequence: 4,
            observedAt: '2026-10-30T00:00:04Z',
            quality: MeasurementQuality.MeasurementQualityStale,
          },
        ],
      }}
      fresh={false}
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.getAllByText(/≈ 1\s?000 Па/)[0]).toBeTruthy();
  expect(screen.getByText(/качество последнего наблюдения: устарело/)).toBeTruthy();
  expect(screen.getByText(/2026-10-30T00:00:01Z/)).toBeTruthy();
  expect(screen.queryByText(/2026-10-30T00:00:04Z/)).toBeNull();
});

it('breaks the chart across an unavailable measurement and labels pending confirmation honestly', () => {
  const view = render(
    <LiveRun record={record} snapshot={snapshot} fresh pending={false} onConfirm={() => {}} />,
  );
  expect(screen.getByTestId('chart').textContent).toBe('1000');
  const unavailable = {
    ...first,
    sequence: 2,
    measuredAt: null,
    value: null,
    quality: MeasurementQuality.MeasurementQualityUnavailable,
  };
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{
        ...snapshot,
        measurementBatch: 2,
        measurementTiming: null,
        measurements: [unavailable],
        pendingAction: {
          testId: record.testId,
          actionId: '44444444-4444-4444-8444-444444444444',
          instruction: 'Подтвердите',
        },
      }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.getByText('Ожидание подтверждения')).toBeTruthy();
  const next = { ...first, sequence: 3, measuredAt: '2026-10-30T00:00:03Z', value: 1002 };
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, measurementBatch: next.sequence, measurements: [next] }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.getByTestId('chart').textContent).toBe('1000,gap,1002');
});

it('keeps adjacent samples connected across snapshot refresh but marks a real connection gap', () => {
  const renderRun = (value: Snapshot, fresh: boolean, gapVersion: number) => (
    <LiveRun
      record={record}
      snapshot={value}
      fresh={fresh}
      gapVersion={gapVersion}
      pending={false}
      onConfirm={() => {}}
    />
  );
  const view = render(renderRun(snapshot, true, 0));
  view.rerender(renderRun(snapshot, false, 0));
  const second = { ...first, sequence: 2, measuredAt: '2026-10-30T00:00:02Z', value: 1001 };
  view.rerender(renderRun({ ...snapshot, measurementBatch: 2, measurements: [second] }, true, 0));
  expect(screen.getByTestId('chart').textContent).toBe('1000,1001');
  view.rerender(renderRun({ ...snapshot, measurementBatch: 2, measurements: [second] }, false, 1));
  const third = { ...first, sequence: 3, measuredAt: '2026-10-30T00:00:03Z', value: 1002 };
  view.rerender(renderRun({ ...snapshot, measurementBatch: 3, measurements: [third] }, true, 1));
  expect(screen.getByTestId('chart').textContent).toBe('1000,1001,gap,1002');
});

it('starts a new chart segment if measurement timestamps move backwards', () => {
  const view = render(
    <LiveRun record={record} snapshot={snapshot} fresh pending={false} onConfirm={() => {}} />,
  );
  const next = { ...first, sequence: 2, measuredAt: '2026-10-29T23:59:00Z', value: 1100 };
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, measurementBatch: next.sequence, measurements: [next] }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.getByTestId('chart').textContent).toBe('1100');
  expect(screen.getByText(/график начат заново/)).toBeTruthy();
});

it('rounds displayed readings without changing chart samples', () => {
  const measured = { ...first, value: 9986.022284815022 };
  render(
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, measurements: [measured] }}
      fresh
      pending={false}
      onConfirm={() => {}}
    />,
  );
  expect(screen.getByText(/≈ 9\s?986,022 Па/)).toBeTruthy();
  expect(screen.getByTestId('chart').textContent).toBe('9986.022284815022');
});
