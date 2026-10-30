import { useEffect, useRef, useState } from 'react';
import { MeasurementChart } from '../components/MeasurementChart';
import { PressureNotation } from '../components/PressureNotation';
import { formatNumber } from '../fixtureAdapter';
import type {
  Measurement,
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { GraphPoint } from '../model';

function shown(item: Measurement | undefined, fresh: boolean): string {
  return fresh && item?.quality === 'valid' && item.value !== null
    ? `≈ ${formatNumber(item.value, 6)} Па`
    : 'Неизвестно';
}

function quality(item: Measurement | undefined, fresh: boolean): string {
  if (!fresh) return 'не подтверждено';
  switch (item?.quality) {
    case 'valid':
      return 'достоверно';
    case 'stale':
      return 'устарело';
    case 'unavailable':
      return 'недоступно';
    default:
      return 'неизвестно';
  }
}

export function LiveRun({
  record,
  snapshot,
  fresh,
  gapVersion = 0,
  pending,
  onConfirm,
}: {
  record: TestRecord;
  snapshot: Snapshot;
  fresh: boolean;
  gapVersion?: number;
  pending: boolean;
  onConfirm: () => void;
}) {
  const [points, setPoints] = useState<GraphPoint[]>([]);
  const [clockChanged, setClockChanged] = useState(false);
  const origin = useRef<number | null>(null);
  const last = useRef<{ sequence: number; seconds: number } | null>(null);
  const gap = useRef(false);
  const observedGapVersion = useRef(gapVersion);
  const pressure = snapshot.measurements?.find((item) => item.channel === 'dd1');
  const differential = snapshot.measurements?.find((item) => item.channel === 'dd2');
  const reserve = snapshot.measurements?.find((item) => item.channel === 'dd3');

  useEffect(() => {
    if (observedGapVersion.current !== gapVersion) {
      observedGapVersion.current = gapVersion;
      gap.current = true;
    }
    if (!fresh) return;
    if (
      pressure?.quality !== 'valid' ||
      pressure.value === null ||
      !pressure.measuredAt ||
      !Number.isSafeInteger(pressure.sequence)
    ) {
      gap.current = true;
      return;
    }
    if (last.current && pressure.sequence <= last.current.sequence) return;
    const timestamp = Date.parse(pressure.measuredAt);
    if (!Number.isFinite(timestamp)) return;
    if (origin.current === null) origin.current = timestamp;
    let seconds = (timestamp - origin.current) / 1000;
    if (last.current && seconds <= last.current.seconds) {
      origin.current = timestamp;
      seconds = 0;
      last.current = null;
      gap.current = false;
      setClockChanged(true);
      setPoints([]);
    }
    const matchingDifferential =
      differential?.quality === 'valid' && differential.measuredAt === pressure.measuredAt
        ? differential.value
        : null;
    const previous = last.current;
    const value = pressure.value;
    const includeGap = gap.current;
    gap.current = false;
    last.current = { sequence: pressure.sequence, seconds };
    setPoints((current) => {
      const result = [...current];
      if (includeGap && previous)
        result.push({
          seconds: (previous.seconds + seconds) / 2,
          pressure: null,
          differential: null,
        });
      result.push({ seconds, pressure: value, differential: matchingDifferential });
      return result.slice(-300);
    });
  }, [pressure, differential, fresh, gapVersion]);

  return (
    <>
      <div className="page-heading run-heading">
        <div>
          <h1>Проведение испытания</h1>
          <p>{record.parameters.valve} · Нагнетание · Без привода</p>
        </div>
      </div>
      <div className="run-grid">
        <aside className="run-stages">
          <div className="section-heading">
            <h2>Этапы</h2>
          </div>
          <p>Упрощённый демонстрационный проход</p>
          <strong>
            {!fresh
              ? 'Состояние не подтверждено'
              : snapshot.pendingAction
                ? 'Ожидание подтверждения'
                : pressure
                  ? 'Измерение'
                  : 'Ожидание измерений'}
          </strong>
        </aside>
        <section className="run-content" aria-label="Измерения">
          <div className="measurement-top">
            <div className="section-heading">
              <h2>Показания</h2>
            </div>
            <div className="measurements live-measurements">
              <div className="reading main-reading">
                <span>
                  Давление в ёмкости <small>ДД1</small>
                </span>
                <strong>{shown(pressure, fresh)}</strong>
                <p>{fresh ? 'Последнее полученное показание' : 'Показание не подтверждено'}</p>
                <p>
                  Время последнего измерения: {pressure?.measuredAt ?? 'неизвестно'} · качество:{' '}
                  {quality(pressure, fresh)}
                </p>
              </div>
              <div className="reading target-reading">
                <span>
                  <PressureNotation /> производителя
                </span>
                <strong>{formatNumber(record.parameters.pmaxPa)} Па</strong>
              </div>
              <div className="reading differential-reading">
                <span>
                  Перепад на диафрагме <small>ДД2</small>
                </span>
                <strong>{shown(differential, fresh)}</strong>
                <p>
                  Время ДД2: {differential?.measuredAt ?? 'неизвестно'} · качество:{' '}
                  {quality(differential, fresh)}
                </p>
                <p>
                  ДД3: {shown(reserve, fresh)} · время: {reserve?.measuredAt ?? 'неизвестно'} ·
                  качество: {quality(reserve, fresh)}
                </p>
              </div>
            </div>
            <p>Показания округлены до шести значащих цифр для отображения.</p>
          </div>
          <div className="chart-panel">
            <div className="section-heading">
              <div>
                <h2>Изменение показаний</h2>
                <p>Время от первого отсчёта по полученным меткам; пропуски обозначены разрывами.</p>
                {clockChanged && (
                  <p>Время измерения изменилось: график начат заново с нового отсчёта.</p>
                )}
              </div>
            </div>
            <MeasurementChart live points={points} target={record.parameters.pmaxPa} />
          </div>
        </section>
        <aside className="action-panel" aria-label="Текущее действие">
          <div>
            <h2>
              {snapshot.pendingAction ? 'Подтверждение оператора' : 'Демонстрационное измерение'}
            </h2>
            <p className="action-instruction">
              {snapshot.pendingAction?.instruction ?? 'Ожидаем состояние и показания ядра.'}
            </p>
            {snapshot.pendingAction && (
              <button
                className="primary-button full-width"
                type="button"
                disabled={!fresh || pending}
                onClick={onConfirm}
              >
                Подтвердить действие
              </button>
            )}
          </div>
        </aside>
      </div>
    </>
  );
}
