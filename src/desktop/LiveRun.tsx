import { type ReactNode, useState } from 'react';
import { RunMeasurementChart, RunScreen } from '../components/RunScreen';
import type {
  Measurement,
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import {
  formatElapsedSeconds,
  formatMeasurement,
  formatNumber,
  formatRemainingSeconds,
} from '../lib/format';
import { measurementQualityLabel } from './display';
import type { MeasurementSeriesContext } from './measurement-series';
import { measurementLabel } from './presentation';
import { useMeasurementSeries } from './use-measurement-series';

function shown(item: Measurement | undefined): string {
  return (item?.quality === 'valid' || item?.quality === 'stale') && item.value !== null
    ? formatMeasurement(item.value)
    : 'Неизвестно';
}
export function LiveRun({
  record,
  snapshot,
  fresh,
  measurementContext,
  measurementKey,
  pending,
  onConfirm,
  stop,
}: {
  record: TestRecord;
  snapshot: Snapshot;
  fresh: boolean;
  measurementContext: MeasurementSeriesContext;
  measurementKey: string;
  pending: boolean;
  onConfirm: () => void;
  stop?: ReactNode;
}) {
  const series = useMeasurementSeries(measurementContext, measurementKey);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const readings = snapshot.currentReadings?.length
    ? snapshot.currentReadings
    : snapshot.measurements;
  const pressure = readings?.find((item) => item.channel === 'dd1');
  const differential = readings?.find(
    (item) => item.channel === (snapshot.activeDifferential ?? 'dd2'),
  );
  const stopping = record.stage === 'stopping';
  const awaiting = !!snapshot.pendingAction;
  const readingNote =
    snapshot.runPhase === 'awaitingValveOpening' || snapshot.runPhase === 'awaitingValveClosing'
      ? 'Показания на момент запроса подтверждения.'
      : undefined;
  const timing = snapshot.phaseTiming ?? snapshot.measurementTiming;
  const phases = [
    ['awaitingPreparation', 'Подготовительные циклы'],
    ['searchingPressure', 'Набор давления'],
    ['stabilizingBeforeOpening', 'Стабилизация перед открытием'],
    ['awaitingValveOpening', 'Открытие клапана'],
    ['awaitingValveClosing', 'Закрытие клапана'],
    ['stabilizingAfterClosing', 'Стабилизация после закрытия'],
    ['measuring', 'Измерение'],
    ['stopping', 'Остановка'],
  ] as const;
  const phase = phases.findIndex(([id]) => id === (stopping ? 'stopping' : snapshot.runPhase));
  const instruction = stopping
    ? 'По последнему снимку: этап остановки. Безопасное давление не подтверждено.'
    : snapshot.pendingAction?.instruction;
  const confirmationLabel = {
    confirmPreparation: 'Подтвердить подготовку',
    confirmValveOpen: 'Подтвердить открытие клапана',
    confirmValveClosed: 'Подтвердить закрытие клапана',
    confirmStopped: 'Подтвердить остановку вала',
  } as const;
  return (
    <RunScreen
      subtitle={<>{record.parameters.valve} · Нагнетание · Без привода</>}
      stages={phases.map(([id, label], index) => ({
        id,
        label,
        state:
          index === phase ? 'current' : stopping ? 'inactive' : index < phase ? 'done' : 'next',
      }))}
      pressure={shown(pressure)}
      differential={shown(differential)}
      differentialChannel={snapshot.activeDifferential === 'dd3' ? 'ДД3' : 'ДД2'}
      readingNote={readingNote}
      target={`${formatNumber(record.parameters.pmaxPa)} Па`}
      pressureNote={<p>{measurementQualityLabel(pressure?.quality)} · последний отсчёт</p>}
      differentialNote={<p>{measurementQualityLabel(differential?.quality)} · последний отсчёт</p>}
      details={
        <details className="reading-details" onToggle={(e) => setDetailsOpen(e.currentTarget.open)}>
          <summary>Время и качество показаний</summary>
          {detailsOpen &&
            readings?.map((item) => (
              <p key={item.channel}>
                {item.channel.replace('dd', 'ДД')}: {shown(item)} ·{' '}
                {item.measuredAt ?? 'время неизвестно'} · качество последнего наблюдения:{' '}
                {measurementQualityLabel(item.quality)}
              </p>
            ))}
        </details>
      }
      chart={
        <RunMeasurementChart
          live
          points={series.points}
          target={record.parameters.pmaxPa}
          note={
            series.clockChanged
              ? 'Время измерения изменилось: график начат заново с нового отсчёта.'
              : 'Время от первого полученного отсчёта; пропуски не соединяются.'
          }
        />
      }
      actionTitle={phases[phase]?.[1] ?? measurementLabel(timing)}
      actionLabel={
        stopping
          ? 'Состояние по последнему полученному снимку'
          : 'Текущее действие по последнему полученному снимку'
      }
      instruction={instruction}
      time={
        <div className="run-timing">
          <dl className="run-time">
            <div>
              <dt>Прошло</dt>
              <dd>{timing ? `${formatElapsedSeconds(timing.elapsedSeconds)} с` : 'Неизвестно'}</dd>
            </div>
            <div>
              <dt>Осталось</dt>
              <dd>
                {timing ? `${formatRemainingSeconds(timing.remainingSeconds)} с` : 'Неизвестно'}
              </dd>
            </div>
          </dl>
        </div>
      }
      action={
        awaiting &&
        !stopping && (
          <button
            className="primary-button full-width"
            type="button"
            disabled={!fresh || pending}
            onClick={onConfirm}
          >
            {snapshot.pendingAction?.kind
              ? confirmationLabel[snapshot.pendingAction.kind]
              : 'Подтвердить действие'}
          </button>
        )
      }
      stop={stop}
    />
  );
}
