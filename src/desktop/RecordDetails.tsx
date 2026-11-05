import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type {
  AuditPage,
  MeasurementPage,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatDateTime, formatNumber } from '../lib/format';
import type { DemoService } from './demo-service';
import { measurementQualityLabel, measurementSourceLabel } from './display';
import { resultReadVersion } from './presentation';

/** Only the open disclosure reads one bounded page. Wrapper replacement keeps the same source. */
export function RecordDetails({
  record,
  service,
  available,
  visible = true,
}: {
  record: TestRecord;
  service: DemoService;
  available: boolean;
  visible?: boolean;
}) {
  const currentService = useRef(service);
  const currentAvailable = useRef(available);
  useLayoutEffect(() => {
    currentService.current = service;
    currentAvailable.current = available;
  }, [service, available]);
  const [measurementOpen, setMeasurementOpen] = useState(false);
  const [informationOpen, setInformationOpen] = useState(false);
  const [auditOpen, setAuditOpen] = useState(false);
  const [measurements, setMeasurements] = useState<MeasurementPage | null>(null);
  const [audit, setAudit] = useState<AuditPage | null>(null);
  const [measurementError, setMeasurementError] = useState('');
  const [auditError, setAuditError] = useState('');
  const [measurementLoading, setMeasurementLoading] = useState(false);
  const [auditLoading, setAuditLoading] = useState(false);
  const measurementGeneration = useRef(0);
  const auditGeneration = useRef(0);
  const measurementCursor = useRef(0);
  const auditCursor = useRef(0);
  const needMeasurements = useRef(false);
  const needAudit = useRef(false);
  const version = resultReadVersion(record);
  const readVersion = useRef(version);
  useEffect(
    () => () => {
      measurementGeneration.current++;
      auditGeneration.current++;
    },
    [],
  );
  const loadMeasurements = useCallback(
    async (after = 0) => {
      if (!currentAvailable.current) {
        setMeasurementError('Ожидание связи для чтения измерений.');
        return;
      }
      const generation = ++measurementGeneration.current;
      measurementCursor.current = after;
      needMeasurements.current = false;
      setMeasurementLoading(true);
      setMeasurementError('');
      try {
        const page = await currentService.current.measurements(record.testId, after, 100);
        if (generation === measurementGeneration.current) setMeasurements(page);
      } catch {
        if (generation === measurementGeneration.current)
          setMeasurementError('Не удалось прочитать измерения.');
      } finally {
        if (generation === measurementGeneration.current) setMeasurementLoading(false);
      }
    },
    [record.testId],
  );
  const loadAudit = useCallback(
    async (after = 0) => {
      if (!currentAvailable.current) {
        setAuditError('Ожидание связи для чтения аудита.');
        return;
      }
      const generation = ++auditGeneration.current;
      auditCursor.current = after;
      needAudit.current = false;
      setAuditLoading(true);
      setAuditError('');
      try {
        const page = await currentService.current.audit(record.testId, after, 100);
        if (generation === auditGeneration.current) setAudit(page);
      } catch {
        if (generation === auditGeneration.current) setAuditError('Не удалось прочитать аудит.');
      } finally {
        if (generation === auditGeneration.current) setAuditLoading(false);
      }
    },
    [record.testId],
  );
  useEffect(() => {
    if (readVersion.current !== version) {
      readVersion.current = version;
      measurementGeneration.current++;
      auditGeneration.current++;
      setMeasurements(null);
      setAudit(null);
      setMeasurementError('');
      setAuditError('');
      setMeasurementLoading(false);
      setAuditLoading(false);
      needMeasurements.current = true;
      needAudit.current = true;
    }
    if (visible && available) {
      if (measurementOpen && needMeasurements.current)
        void loadMeasurements(measurementCursor.current);
      if (auditOpen && needAudit.current) void loadAudit(auditCursor.current);
    }
  }, [version, visible, available, measurementOpen, auditOpen, loadMeasurements, loadAudit]);
  if (!visible) return null;
  return (
    <div className="record-disclosures">
      <details
        className="work-surface result-section"
        open={informationOpen}
        onToggle={(event) => setInformationOpen(event.currentTarget.open)}
      >
        <summary>Сведения о записи</summary>
        <dl className="result-details">
          <div>
            <dt>Идентификатор</dt>
            <dd>{record.testId}</dd>
          </div>
          <div>
            <dt>Создано</dt>
            <dd>{formatDateTime(record.createdAt)}</dd>
          </div>
          <div>
            <dt>Оператор demo</dt>
            <dd>{record.operatorId}</dd>
          </div>
        </dl>
      </details>
      <details
        className="work-surface result-section"
        open={measurementOpen}
        onToggle={(e) => {
          setMeasurementOpen(e.currentTarget.open);
          if (e.currentTarget.open && !measurements && !measurementLoading)
            needMeasurements.current = true;
        }}
      >
        <summary>Измерения</summary>
        {measurementOpen && (
          <>
            <p>Страница до 100 отсчётов. Среднее, расход и класс не рассчитываются.</p>
            {measurementError && <p role="alert">{measurementError}</p>}
            {measurementLoading && <p role="status">Чтение измерений…</p>}
            {measurements && (
              <div className="table-scroll">
                <table className="work-table">
                  <thead>
                    <tr>
                      <th>Канал</th>
                      <th>Значение</th>
                      <th>Качество</th>
                      <th>Время измерения</th>
                      <th>Источник</th>
                    </tr>
                  </thead>
                  <tbody>
                    {measurements.items?.map((item) => (
                      <tr key={item.sequence}>
                        <td>{item.channel.replace('dd', 'ДД')}</td>
                        <td>
                          {item.value === null ? 'Неизвестно' : formatNumber(item.value)}{' '}
                          {item.unit}
                        </td>
                        <td>{measurementQualityLabel(item.quality)}</td>
                        <td>{item.measuredAt ?? 'Время неизвестно'}</td>
                        <td>{measurementSourceLabel(item.source)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {!measurements.items?.length && <p>На этой странице нет сохранённых измерений.</p>}
              </div>
            )}
            <div className="history-pages">
              <button
                type="button"
                disabled={measurementLoading}
                onClick={() => void loadMeasurements()}
              >
                К началу измерений
              </button>
              {!!measurements?.nextAfter && (
                <button
                  type="button"
                  disabled={measurementLoading}
                  onClick={() => void loadMeasurements(measurements.nextAfter ?? 0)}
                >
                  Следующая страница измерений
                </button>
              )}
            </div>
          </>
        )}
      </details>
      <details
        className="work-surface result-section"
        open={auditOpen}
        onToggle={(e) => {
          setAuditOpen(e.currentTarget.open);
          if (e.currentTarget.open && !audit && !auditLoading) needAudit.current = true;
        }}
      >
        <summary>Аудит</summary>
        {auditOpen && (
          <>
            <p>Страница до 100 событий.</p>
            {auditError && <p role="alert">{auditError}</p>}
            {auditLoading && <p role="status">Чтение аудита…</p>}
            <ul>
              {audit?.items?.map((item) => (
                <li key={item.sequence}>
                  {item.occurredAt} · {item.action} · {item.outcome} · {item.operatorId}
                  {item.commandId ? ` · команда ${item.commandId}` : ''}
                  {item.actionId ? ` · действие ${item.actionId}` : ''}
                  {item.reason
                    ? ` · причина ${item.reason === 'canceled_by_stop' ? 'запуск отменён при остановке до подтверждения оператора' : item.reason}`
                    : ''}
                </li>
              ))}
            </ul>
            {audit && !audit.items?.length && <p>На этой странице нет сохранённых событий.</p>}
            <div className="history-pages">
              <button type="button" disabled={auditLoading} onClick={() => void loadAudit()}>
                К началу аудита
              </button>
              {!!audit?.nextAfter && (
                <button
                  type="button"
                  disabled={auditLoading}
                  onClick={() => void loadAudit(audit.nextAfter ?? 0)}
                >
                  Следующая страница аудита
                </button>
              )}
            </div>
          </>
        )}
      </details>
    </div>
  );
}
