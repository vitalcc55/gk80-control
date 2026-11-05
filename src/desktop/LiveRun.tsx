import { type ReactNode, useState } from 'react';
import { RunScreen } from '../components/RunScreen';
import type {
  Measurement,
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatElapsedSeconds, formatNumber, formatRemainingSeconds } from '../lib/format';
import type { MeasurementSeries } from './measurement-series';

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
  series,
  pending,
  onConfirm,
  stop,
}: {
  record: TestRecord;
  snapshot: Snapshot;
  fresh: boolean;
  series: MeasurementSeries;
  pending: boolean;
  onConfirm: () => void;
  stop?: ReactNode;
}) {
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
      pressureNote={<p>{!fresh ? 'Актуальность не подтверждена' : quality(pressure)}</p>}
      differentialNote={<p>{!fresh ? 'Актуальность не подтверждена' : quality(differential)}</p>}
      readingNote={
        fresh
          ? 'Показания симулятора · округление до 6 значащих цифр'
          : 'Последние полученные показания · устарело; текущие значения не подтверждены.'
      }
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
      instruction={
        stopping
          ? 'Остановка выполняется; безопасное давление не подтверждено.'
          : !fresh
            ? 'Данные устарели. Ожидаем подтверждение состояния ядром.'
            : (snapshot.pendingAction?.instruction ??
              (pressure
                ? 'Идёт сбор показаний симулятора. Дождитесь завершения измерения.'
                : 'Ожидаем первые показания ядра.'))
      }
      time={
        <dl className="run-time">
          <p>Время на последнем принятом отсчёте ядра</p>
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
          {!fresh && timing && <p>Время по последнему снимку · устарело</p>}
        </dl>
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
