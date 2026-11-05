import { MeasurementChart } from '../components/MeasurementChart';
import { PressureNotation } from '../components/PressureNotation';
import { formatNumber } from '../fixtureAdapter';
import type {
  Measurement,
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { MeasurementSeries } from './measurement-series';

function shown(item: Measurement | undefined): string {
  return (item?.quality === 'valid' || item?.quality === 'stale') && item.value !== null
    ? `≈ ${formatNumber(item.value, 6)} Па`
    : 'Неизвестно';
}

function quality(item: Measurement | undefined): string {
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
  series,
  pending,
  onConfirm,
}: {
  record: TestRecord;
  snapshot: Snapshot;
  fresh: boolean;
  series: MeasurementSeries;
  pending: boolean;
  onConfirm: () => void;
}) {
  const { points, clockChanged } = series;
  const pressure = snapshot.measurements?.find((item) => item.channel === 'dd1');
  const differential = snapshot.measurements?.find((item) => item.channel === 'dd2');
  const reserve = snapshot.measurements?.find((item) => item.channel === 'dd3');

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
          <p>Этап по последнему полученному снимку</p>
          <strong>
            {snapshot.pendingAction
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
              <p>Последние полученные показания; текущие значения могут отличаться.</p>
            </div>
            <div className="measurements live-measurements">
              <div className="reading main-reading">
                <span>
                  Давление в ёмкости <small>ДД1</small>
                </span>
                <strong>{shown(pressure)}</strong>
                <p>
                  Время последнего измерения: {pressure?.measuredAt ?? 'неизвестно'} · качество
                  последнего наблюдения: {quality(pressure)}
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
                <strong>{shown(differential)}</strong>
                <p>
                  Время ДД2: {differential?.measuredAt ?? 'неизвестно'} · качество последнего
                  наблюдения: {quality(differential)}
                </p>
                <p>
                  ДД3: {shown(reserve)} · время: {reserve?.measuredAt ?? 'неизвестно'} · качество
                  последнего наблюдения: {quality(reserve)}
                </p>
              </div>
            </div>
            <p>Показания округлены до шести значащих цифр для отображения.</p>
          </div>
          <div className="chart-panel">
            <div className="section-heading">
              <div>
                <h2>Изменение показаний</h2>
                <p>Время от первого полученного отсчёта; при потере связи график прерывается.</p>
                {clockChanged && (
                  <p>Время измерения изменилось: график начат заново с нового отсчёта.</p>
                )}
              </div>
            </div>
            <MeasurementChart live points={points} target={record.parameters.pmaxPa} />
          </div>
        </section>
        <aside className="action-panel" aria-label="Действие по последнему полученному снимку">
          <div>
            <p>Действие по последнему полученному снимку</p>
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
