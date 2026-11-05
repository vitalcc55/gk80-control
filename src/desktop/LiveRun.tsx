import { type ReactNode, useState } from 'react';
import { RunScreen } from '../components/RunScreen';
import type {
  Measurement,
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatElapsedSeconds, formatNumber, formatRemainingSeconds } from '../lib/format';
import type { MeasurementSeriesContext } from './measurement-series';
import { useMeasurementSeries } from './use-measurement-series';

function shown(item: Measurement | undefined): string {
  return (item?.quality === 'valid' || item?.quality === 'stale') && item.value !== null
    ? `≈ ${formatNumber(item.value, 6)} Па`
    : 'Неизвестно';
}
function quality(item: Measurement | undefined): string {
  return item?.quality === 'valid'
    ? 'достоверно'
    : item?.quality === 'stale'
      ? 'устарело'
      : item?.quality === 'unavailable'
        ? 'недоступно'
        : 'неизвестно';
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
  const pressure = snapshot.measurements?.find((item) => item.channel === 'dd1');
  const differential = snapshot.measurements?.find((item) => item.channel === 'dd2');
  const stopping = record.stage === 'stopping';
  const awaiting = !!snapshot.pendingAction;
  const timing = snapshot.measurementTiming;
  const phase = stopping ? 2 : awaiting ? 0 : 1;
  return (
    <RunScreen
      subtitle={<>{record.parameters.valve} · Нагнетание · Без привода</>}
      stages={['Ожидание подтверждения', 'Демонстрационное измерение', 'Остановка'].map(
        (label, index) => ({
          id: String(index),
          label,
          state:
            index === phase ? 'current' : stopping ? 'inactive' : index < phase ? 'done' : 'next',
        }),
      )}
      stageNote={<p>Этап по последнему полученному снимку</p>}
      pressure={shown(pressure)}
      differential={shown(differential)}
      target={`${formatNumber(record.parameters.pmaxPa)} Па`}
      pressureNote={<p>{quality(pressure)} · последний отсчёт</p>}
      differentialNote={<p>{quality(differential)} · последний отсчёт</p>}
      readingNote="Последние полученные показания · округление до 6 значащих цифр"
      details={
        <details className="reading-details" onToggle={(e) => setDetailsOpen(e.currentTarget.open)}>
          <summary>Время и качество показаний</summary>
          {detailsOpen &&
            snapshot.measurements?.map((item) => (
              <p key={item.channel}>
                {item.channel.replace('dd', 'ДД')}: {shown(item)} ·{' '}
                {item.measuredAt ?? 'время неизвестно'} · качество последнего наблюдения:{' '}
                {quality(item)}
              </p>
            ))}
        </details>
      }
      chart={{ live: true, points: series.points, target: record.parameters.pmaxPa }}
      chartNote={
        series.clockChanged
          ? 'Время измерения изменилось: график начат заново с нового отсчёта.'
          : 'Время от первого полученного отсчёта; пропуски не соединяются.'
      }
      actionTitle={
        stopping ? 'Остановка' : awaiting ? 'Подтверждение оператора' : 'Демонстрационное измерение'
      }
      actionLabel="Текущее действие по последнему полученному снимку"
      instruction={
        stopping
          ? 'По последнему снимку: остановка выполняется; безопасное давление не подтверждено.'
          : (snapshot.pendingAction?.instruction ??
            (pressure
              ? 'Идёт сбор показаний симулятора. Дождитесь завершения измерения.'
              : 'Ожидаем первые показания ядра.'))
      }
      time={
        <div className="run-timing">
          <p>Время на последнем принятом отсчёте ядра</p>
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
            Подтвердить действие
          </button>
        )
      }
      stop={stop}
    />
  );
}
