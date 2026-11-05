import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { type ComponentProps, useState } from 'react';
import { expect, it, vi } from 'vitest';
import { LiveRun as LiveRunView } from '../src/desktop/LiveRun';
import { MeasurementSeriesContext } from '../src/desktop/measurement-series';
import { useMeasurementSeries } from '../src/desktop/use-measurement-series';

function LiveRun({
  gapVersion = 0,
  ...props
}: Omit<ComponentProps<typeof LiveRunView>, 'series'> & { gapVersion?: number }) {
  const [context] = useState(() => new MeasurementSeriesContext());
  const series = useMeasurementSeries(
    context,
    props.record.testId,
    props.snapshot,
    props.fresh,
    gapVersion,
  );
  return <LiveRunView {...props} series={series} />;
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
  measurementTiming: null,
  measurements: [first],
};

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
  expect(within(reading).getByText('устарело')).toBeTruthy();
  expect(screen.getByText('Время на последнем принятом отсчёте ядра')).toBeTruthy();
  expect(screen.getByText('5 с')).toBeTruthy();
  expect(screen.getByText('55 с')).toBeTruthy();
});

it('does not invent completed measurement or pressure reduction after an early Stop', () => {
  render(
    <LiveRun
      record={{ ...record, stage: TestRecordStage.Stopping }}
      snapshot={{ ...snapshot, measurements: [], measurementTiming: null }}
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
  expect(screen.getAllByText(/≈ 1\s?000 Па/)[0]).toBeTruthy();
  expect(screen.getByText('≈ 150 Па')).toBeTruthy();
  expect(screen.queryByText(/ДД3:.*≈ 80 Па/)).toBeNull();
  await userEvent.setup().click(screen.getByText('Время и качество показаний'));
  expect(await screen.findByText(/ДД3:.*≈ 80 Па/)).toBeTruthy();
  expect(screen.getByText(/Последние полученные показания/)).toBeTruthy();
  expect(screen.getAllByText(/качество последнего наблюдения: достоверно/)).toHaveLength(3);
  expect(screen.getAllByText(/2026-10-30T00:00:01Z/)).toHaveLength(3);
  expect(screen.getByText('Этап по последнему полученному снимку')).toBeTruthy();
  expect(
    screen.getByRole('button', { name: 'Подтвердить действие' }).hasAttribute('disabled'),
  ).toBe(true);
  expect(screen.getByTestId('chart').textContent).toBe('1000');
  const next = { ...first, sequence: 2, measuredAt: '2026-10-30T00:00:02Z', value: 1001 };
  view.rerender(
    <LiveRun
      record={record}
      snapshot={{ ...snapshot, measurements: [next] }}
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
      snapshot={{ ...snapshot, measurements: [next] }}
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
  view.rerender(renderRun({ ...snapshot, measurements: [second] }, true, 0));
  expect(screen.getByTestId('chart').textContent).toBe('1000,1001');
  view.rerender(renderRun({ ...snapshot, measurements: [second] }, false, 1));
  const third = { ...first, sequence: 3, measuredAt: '2026-10-30T00:00:03Z', value: 1002 };
  view.rerender(renderRun({ ...snapshot, measurements: [third] }, true, 1));
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
      snapshot={{ ...snapshot, measurements: [next] }}
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
  expect(screen.getByText(/≈ 9\s?986,02 Па/)).toBeTruthy();
  expect(screen.getByTestId('chart').textContent).toBe('9986.022284815022');
});
