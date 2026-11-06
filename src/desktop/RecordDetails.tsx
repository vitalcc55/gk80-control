import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type {
  AuditEvent,
  AuditPage,
  MeasurementPage,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatDateTime, formatEventDateTime, formatNumber } from '../lib/format';
import type { DemoService } from './demo-service';
import {
  eventActionLabel,
  eventActorLabel,
  eventOutcomeLabel,
  eventReasonLabels,
  measurementQualityLabel,
  measurementSourceLabel,
} from './display';
import { resultReadVersion } from './presentation';

const EventRow = memo(function EventRow({ item }: { item: AuditEvent }) {
  const [open, setOpen] = useState(false);
  return (
    <li>
      <p>
        {eventActionLabel(item.action)} — {eventOutcomeLabel(item.outcome)}
      </p>
      <p>
        <time>{formatEventDateTime(item.occurredAt)}</time> · {eventActorLabel(item.operatorId)}
      </p>
      {item.reason && <p>{eventReasonLabels(item.reason).join('; ')}</p>}
      <details onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary>Подробности события</summary>
        {open && (
          <dl className="result-details">
            <div><dt>Исходное время</dt><dd>{item.occurredAt}</dd></div>
            <div><dt>Код события</dt><dd>{item.action}</dd></div>
            <div><dt>Код результата</dt><dd>{item.outcome}</dd></div>
            <div><dt>Идентификатор участника</dt><dd>{item.operatorId}</dd></div>
            <div><dt>Испытание</dt><dd>{item.testId}</dd></div>
            <div><dt>Номер события</dt><dd>{item.sequence}</dd></div>
            <div><dt>Версия</dt><dd>{item.revision}</dd></div>
            {item.commandId && <div><dt>Команда</dt><dd>{item.commandId}</dd></div>}
            {item.actionId && <div><dt>Действие</dt><dd>{item.actionId}</dd></div>}
            {item.reason && <div><dt>Коды причин</dt><dd>{item.reason}</dd></div>}
            {item.instruction && <div><dt>Исходная инструкция</dt><dd>{item.instruction}</dd></div>}
          </dl>
        )}
      </details>
    </li>
  );
});

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
  const [measurementRetryVisible, setMeasurementRetryVisible] = useState(false);
  const [measurementRetry, setMeasurementRetry] = useState<{
    after: number;
    returnToStart: boolean;
  } | null>(null);
  const [auditError, setAuditError] = useState('');
  const [measurementLoading, setMeasurementLoading] = useState(false);
  const [measurementStale, setMeasurementStale] = useState(false);
  const [measurementRefresh, setMeasurementRefresh] = useState(0);
  const [auditLoading, setAuditLoading] = useState(false);
  const [auditStale, setAuditStale] = useState(false);
  const [auditRefresh, setAuditRefresh] = useState(0);
  const measurementGeneration = useRef(0);
  const measurementInFlight = useRef(false);
  const activeMeasurement = useRef<{
    after: number;
    returnToStart: boolean;
    manual: boolean;
  } | null>(null);
  const scrollAfterMeasurement = useRef<number | null>(null);
  const measurementTable = useRef<HTMLDivElement>(null);
  const measurementDetails = useRef<HTMLDetailsElement>(null);
  const measurementRetryButton = useRef<HTMLButtonElement>(null);
  const mounted = useRef(false);
  const auditGeneration = useRef(0);
  const auditInFlight = useRef(false);
  const measurementCursor = useRef(0);
  const auditCursor = useRef(0);
  const auditRequestCursor = useRef<number | null>(null);
  const needMeasurements = useRef(false);
  const needAudit = useRef(false);
  const version = resultReadVersion(record);
  const readVersion = useRef(version);
  const wasVisible = useRef(visible);
  const wasAvailable = useRef(available);
  useLayoutEffect(() => {
    if (wasAvailable.current && !available && auditInFlight.current) {
      auditGeneration.current++;
      needAudit.current = true;
    }
    if (
      (wasVisible.current && !visible) ||
      (wasAvailable.current &&
        !available &&
        (measurementInFlight.current || scrollAfterMeasurement.current !== null))
    ) {
      measurementGeneration.current++;
      scrollAfterMeasurement.current = null;
      if (measurementInFlight.current) needMeasurements.current = true;
      if (visible && wasAvailable.current && !available && activeMeasurement.current?.manual) {
        const { after, returnToStart } = activeMeasurement.current;
        setMeasurementRetry({ after, returnToStart });
        setMeasurementRetryVisible(true);
        setMeasurementError('Связь не подтверждена. Повторите чтение измерений.');
      }
      if (!visible) {
        setMeasurementLoading(false);
        activeMeasurement.current = null;
        setMeasurementRetry(null);
        setMeasurementError('');
      }
    }
    wasVisible.current = visible;
    wasAvailable.current = available;
  }, [visible, available]);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      measurementGeneration.current++;
      auditGeneration.current++;
    };
  }, []);
  const loadMeasurements = useCallback(
    async (after = 0, returnToStart = false, manual = false) => {
      if (measurementInFlight.current) return;
      if (!currentAvailable.current) {
        setMeasurementError('Ожидание связи для чтения измерений.');
        setMeasurementRetryVisible(true);
        setMeasurementRetry({ after, returnToStart });
        return;
      }
      const generation = ++measurementGeneration.current;
      measurementInFlight.current = true;
      activeMeasurement.current = { after, returnToStart, manual };
      needMeasurements.current = false;
      setMeasurementLoading(true);
      setMeasurementError('');
      setMeasurementRetry(null);
      try {
        const page = await currentService.current.measurements(record.testId, after, 100);
        if (generation === measurementGeneration.current && measurementDetails.current?.open) {
          measurementCursor.current = after;
          scrollAfterMeasurement.current = returnToStart ? generation : null;
          setMeasurements(page);
          setMeasurementStale(false);
          setMeasurementRetry(null);
        } else if (generation === measurementGeneration.current) needMeasurements.current = true;
      } catch {
        if (generation === measurementGeneration.current && measurementDetails.current?.open) {
          setMeasurementError('Не удалось прочитать измерения.');
          setMeasurementRetryVisible(true);
          setMeasurementRetry({ after, returnToStart });
        }
      } finally {
        measurementInFlight.current = false;
        activeMeasurement.current = null;
        if (mounted.current) {
          setMeasurementLoading(false);
          if (generation !== measurementGeneration.current || needMeasurements.current)
            setMeasurementRefresh((value) => value + 1);
        }
      }
    },
    [record.testId],
  );
  const loadAudit = useCallback(
    async (after = 0) => {
      if (auditInFlight.current) return;
      auditRequestCursor.current = after;
      if (!currentAvailable.current) {
        needAudit.current = true;
        setAuditError('Ожидание связи для чтения журнала.');
        return;
      }
      const generation = ++auditGeneration.current;
      auditInFlight.current = true;
      needAudit.current = false;
      setAuditLoading(true);
      setAuditError('');
      try {
        const page = await currentService.current.audit(record.testId, after, 100);
        if (generation === auditGeneration.current) {
          auditCursor.current = after;
          auditRequestCursor.current = null;
          setAudit(page);
          setAuditStale(false);
        }
      } catch {
        if (generation === auditGeneration.current) setAuditError('Не удалось прочитать журнал.');
      } finally {
        auditInFlight.current = false;
        if (mounted.current) {
          setAuditLoading(false);
          if (generation !== auditGeneration.current && needAudit.current)
            setAuditRefresh((value) => value + 1);
        }
      }
    },
    [record.testId],
  );
  useLayoutEffect(() => {
    if (readVersion.current !== version) {
      readVersion.current = version;
      measurementGeneration.current++;
      scrollAfterMeasurement.current = null;
      auditGeneration.current++;
      setMeasurementStale(true);
      setAuditStale(true);
      if (activeMeasurement.current?.manual) {
        const { after, returnToStart } = activeMeasurement.current;
        setMeasurementRetry({ after, returnToStart });
        setMeasurementRetryVisible(true);
        setMeasurementError('Данные обновились. Повторите чтение измерений.');
      }
      setAuditError('');
      setMeasurementLoading(measurementInFlight.current);
      setAuditLoading(auditInFlight.current);
      needMeasurements.current = true;
      needAudit.current = true;
    }
    if (visible && available) {
      if (
        measurementOpen &&
        measurementDetails.current?.open &&
        needMeasurements.current &&
        !measurementInFlight.current &&
        measurementRetry === null
      )
        void loadMeasurements(measurementCursor.current);
      if (auditOpen && needAudit.current && !auditInFlight.current)
        void loadAudit(auditRequestCursor.current ?? auditCursor.current);
    }
  }, [
    version,
    visible,
    available,
    measurementOpen,
    auditOpen,
    loadMeasurements,
    loadAudit,
    measurementRefresh,
    measurementRetry,
    auditRefresh,
  ]);
  useLayoutEffect(() => {
    if (
      scrollAfterMeasurement.current !== null &&
      scrollAfterMeasurement.current === measurementGeneration.current &&
      visible &&
      measurementOpen &&
      measurementDetails.current?.open &&
      measurementTable.current
    ) {
      measurementTable.current.scrollTop = 0;
      scrollAfterMeasurement.current = null;
    }
  }, [measurements, measurementLoading, visible, measurementOpen]);
  useEffect(() => {
    if (
      measurementRetryVisible &&
      !measurementRetry &&
      !measurementLoading &&
      document.activeElement !== measurementRetryButton.current
    )
      setMeasurementRetryVisible(false);
  }, [measurementRetryVisible, measurementRetry, measurementLoading]);
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
            <dt>Оператор</dt>
            <dd>{eventActorLabel(record.operatorId)}</dd>
          </div>
          <div>
            <dt>Идентификатор оператора</dt>
            <dd>{record.operatorId}</dd>
          </div>
        </dl>
      </details>
      <details
        className="work-surface result-section"
        ref={measurementDetails}
        open={measurementOpen}
        onToggle={(e) => {
          const open = e.currentTarget.open;
          setMeasurementOpen(open);
          if (!open) {
            measurementGeneration.current++;
            scrollAfterMeasurement.current = null;
            if (measurementInFlight.current) needMeasurements.current = true;
            activeMeasurement.current = null;
            setMeasurementLoading(false);
            setMeasurementRetry(null);
            setMeasurementError('');
          }
          if (open) {
            if (measurementInFlight.current) setMeasurementLoading(true);
            if (!measurements || measurementStale) needMeasurements.current = true;
            if (needMeasurements.current && !measurementInFlight.current)
              setMeasurementRefresh((value) => value + 1);
          }
        }}
      >
        <summary
          onClick={() => {
            if (measurementDetails.current?.open) {
              measurementGeneration.current++;
              scrollAfterMeasurement.current = null;
              if (measurementInFlight.current) needMeasurements.current = true;
              activeMeasurement.current = null;
              setMeasurementRetry(null);
              setMeasurementError('');
            }
          }}
        >
          Измерения
        </summary>
        {measurementOpen && (
          <>
            <p>Страница до 100 отсчётов. Среднее, расход и класс не рассчитываются.</p>
            {measurementError && <p role="alert">{measurementError}</p>}
            {measurementRetryVisible && (
              <button
                type="button"
                className="measurement-retry"
                ref={measurementRetryButton}
                aria-disabled={measurementLoading || measurementRetry === null}
                onBlur={() => {
                  if (!measurementRetry && !measurementLoading) setMeasurementRetryVisible(false);
                }}
                onClick={() => {
                  if (measurementRetry)
                    void loadMeasurements(
                      measurementRetry.after,
                      measurementRetry.returnToStart,
                      true,
                    );
                }}
              >
                Повторить чтение измерений
              </button>
            )}
            {measurementLoading && <p role="status">Чтение измерений…</p>}
            {measurementStale && measurements && <p>Показана предыдущая страница измерений.</p>}
            {measurements && (
              <div className="table-scroll" ref={measurementTable}>
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
                aria-disabled={measurementLoading}
                onClick={() => void loadMeasurements(0, true, true)}
              >
                К началу измерений
              </button>
              {measurements && (
                <button
                  type="button"
                  aria-disabled={measurementLoading || !measurements.nextAfter}
                  onClick={() => {
                    if (measurements.nextAfter)
                      void loadMeasurements(measurements.nextAfter, false, true);
                  }}
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
          if (e.target !== e.currentTarget) return;
          setAuditOpen(e.currentTarget.open);
          if (e.currentTarget.open && (!audit || auditStale) && !auditLoading)
            needAudit.current = true;
        }}
      >
        <summary>Журнал событий</summary>
        {auditOpen && (
          <>
            <p>Страница до 100 событий.</p>
            {auditError && <p role="alert">{auditError}</p>}
            {auditLoading && <p role="status">Чтение журнала…</p>}
            {auditStale && audit && <p>Показана предыдущая страница журнала.</p>}
            <ul>
              {audit?.items?.map((item) => <EventRow key={item.sequence} item={item} />)}
            </ul>
            {audit && !audit.items?.length && <p>На этой странице нет сохранённых событий.</p>}
            <div className="history-pages">
              <button type="button" aria-disabled={auditLoading} onClick={() => void loadAudit()}>
                К началу журнала
              </button>
              {audit && (
                <button
                  type="button"
                  aria-disabled={auditLoading || !audit.nextAfter}
                  onClick={() => {
                    if (audit.nextAfter) void loadAudit(audit.nextAfter);
                  }}
                >
                  Следующая страница журнала
                </button>
              )}
            </div>
          </>
        )}
      </details>
    </div>
  );
}
