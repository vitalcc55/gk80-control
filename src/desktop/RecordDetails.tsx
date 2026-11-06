import { memo, useEffect, useLayoutEffect, useRef, useState } from 'react';
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
import { useRecordPage } from './use-record-page';

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
            <div>
              <dt>Исходное время</dt>
              <dd>{item.occurredAt}</dd>
            </div>
            <div>
              <dt>Код события</dt>
              <dd>{item.action}</dd>
            </div>
            <div>
              <dt>Код результата</dt>
              <dd>{item.outcome}</dd>
            </div>
            <div>
              <dt>Идентификатор участника</dt>
              <dd>{item.operatorId}</dd>
            </div>
            <div>
              <dt>Испытание</dt>
              <dd>{item.testId}</dd>
            </div>
            <div>
              <dt>Номер события</dt>
              <dd>{item.sequence}</dd>
            </div>
            <div>
              <dt>Версия</dt>
              <dd>{item.revision}</dd>
            </div>
            {item.commandId && (
              <div>
                <dt>Команда</dt>
                <dd>{item.commandId}</dd>
              </div>
            )}
            {item.actionId && (
              <div>
                <dt>Действие</dt>
                <dd>{item.actionId}</dd>
              </div>
            )}
            {item.reason && (
              <div>
                <dt>Коды причин</dt>
                <dd>{item.reason}</dd>
              </div>
            )}
            {item.instruction && (
              <div>
                <dt>Исходная инструкция</dt>
                <dd>{item.instruction}</dd>
              </div>
            )}
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
  const [informationOpen, setInformationOpen] = useState(false);
  const [measurementRetryVisible, setMeasurementRetryVisible] = useState(false);
  const measurementTable = useRef<HTMLDivElement>(null);
  const measurementRetryButton = useRef<HTMLButtonElement>(null);
  const version = resultReadVersion(record);
  const measurement = useRecordPage<MeasurementPage>({
    id: record.testId,
    version,
    visible,
    available,
    kind: 'measurements',
    read: (id, after) => service.measurements(id, after, 100),
  });
  const journal = useRecordPage<AuditPage>({
    id: record.testId,
    version,
    visible,
    available,
    kind: 'audit',
    read: (id, after) => service.audit(id, after, 100),
  });
  const measurementOpen = measurement.open;
  const measurements = measurement.page;
  const measurementError = measurement.error;
  const measurementRetry = measurement.retry;
  const measurementLoading = measurement.loading;
  const measurementStale = measurement.stale;
  const auditOpen = journal.open;
  const audit = journal.page;
  const auditError = journal.error;
  const auditLoading = journal.loading;
  const auditStale = journal.stale;
  useLayoutEffect(() => {
    if (
      measurement.scrollVersion &&
      measurementOpen &&
      visible &&
      available &&
      measurementTable.current &&
      measurement.consumeScroll()
    ) {
      measurementTable.current.scrollTop = 0;
    }
  }, [measurement.scrollVersion, measurement.consumeScroll, measurementOpen, visible, available]);
  useEffect(() => {
    if (measurementRetry) setMeasurementRetryVisible(true);
    else if (
      measurementRetryVisible &&
      !measurementLoading &&
      document.activeElement !== measurementRetryButton.current
    )
      setMeasurementRetryVisible(false);
  }, [measurementRetry, measurementRetryVisible, measurementLoading]);
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
        open={measurementOpen}
        onToggle={(event) => {
          if (event.target === event.currentTarget) measurement.setOpen(event.currentTarget.open);
        }}
      >
        {/* biome-ignore lint/a11y/noStaticElementInteractions: native summary is keyboard interactive. */}
        <summary
          onClick={(event) => {
            if (event.currentTarget.closest('details')?.open) measurement.beforeClose();
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
                  if (measurementRetry) measurement.retryRead();
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
                onClick={() => measurement.request(0, true)}
              >
                К началу измерений
              </button>
              {measurements && (
                <button
                  type="button"
                  aria-disabled={measurementLoading || !measurements.nextAfter}
                  onClick={() => {
                    if (measurements.nextAfter) measurement.request(measurements.nextAfter);
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
        onToggle={(event) => {
          if (event.target === event.currentTarget) journal.setOpen(event.currentTarget.open);
        }}
      >
        {/* biome-ignore lint/a11y/noStaticElementInteractions: native summary is keyboard interactive. */}
        <summary
          onClick={(event) => {
            if (event.currentTarget.closest('details')?.open) journal.beforeClose();
          }}
        >
          Журнал событий
        </summary>
        {auditOpen && (
          <>
            <p>Страница до 100 событий.</p>
            {auditError && <p role="alert">{auditError}</p>}
            {auditLoading && <p role="status">Чтение журнала…</p>}
            {auditStale && audit && <p>Показана предыдущая страница журнала.</p>}
            <ul>
              {audit?.items?.map((item) => (
                <EventRow key={item.sequence} item={item} />
              ))}
            </ul>
            {audit && !audit.items?.length && <p>На этой странице нет сохранённых событий.</p>}
            <div className="history-pages">
              <button type="button" aria-disabled={auditLoading} onClick={() => journal.request()}>
                К началу журнала
              </button>
              {audit && (
                <button
                  type="button"
                  aria-disabled={auditLoading || !audit.nextAfter}
                  onClick={() => {
                    if (audit.nextAfter) journal.request(audit.nextAfter);
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
