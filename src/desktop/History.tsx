import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type {
  AuditPage,
  MeasurementPage,
  TestList,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DemoService } from './demo-service';
import { outcomeLabel, qualityLabel, saveLabel } from './display';

export function History({
  onNew,
  onCopy,
  onDiscard,
  disabled,
  available,
  draftAvailable,
  service,
}: {
  onNew: () => void;
  onCopy: (parameters: TestRecord['parameters']) => void;
  onDiscard: () => void;
  disabled: boolean;
  available: boolean;
  draftAvailable: boolean;
  service: DemoService;
}) {
  const [list, setList] = useState<TestList | null>(null);
  const [selected, setSelected] = useState<TestRecord | null>(null);
  const [measurements, setMeasurements] = useState<MeasurementPage | null>(null);
  const [audit, setAudit] = useState<AuditPage | null>(null);
  const [error, setError] = useState('');
  const [discarding, setDiscarding] = useState(false);
  const selectedGeneration = useRef(0);
  const listGeneration = useRef(0);
  const measurementsGeneration = useRef(0);
  const auditGeneration = useRef(0);
  const listReady = useRef(false);
  const currentService = useRef(service);
  useLayoutEffect(() => {
    currentService.current = service;
  }, [service]);

  useEffect(
    () => () => {
      selectedGeneration.current += 1;
      measurementsGeneration.current += 1;
      auditGeneration.current += 1;
    },
    [],
  );

  const load = useCallback(async (after?: string) => {
    const generation = ++listGeneration.current;
    try {
      const page = await currentService.current.listTests({ limit: 100, after: after ?? null });
      if (generation !== listGeneration.current) return;
      listReady.current = true;
      setList(page);
      setError('');
    } catch {
      if (generation === listGeneration.current)
        setError('Не удалось прочитать список испытаний из ядра.');
    }
  }, []);
  useEffect(() => {
    if (available && !listReady.current) void load();
    return () => {
      listGeneration.current += 1;
    };
  }, [load, available]);
  async function open(testId: string) {
    const generation = ++selectedGeneration.current;
    measurementsGeneration.current += 1;
    auditGeneration.current += 1;
    setSelected(null);
    setMeasurements(null);
    setAudit(null);
    setError('');
    try {
      const record = await currentService.current.test(testId);
      if (generation !== selectedGeneration.current) return;
      const [measurementPage, auditPage] = await Promise.all([
        currentService.current.measurements(testId, 0, 100),
        currentService.current.audit(testId, 0, 100),
      ]);
      if (generation !== selectedGeneration.current) return;
      setSelected(record);
      setMeasurements(measurementPage);
      setAudit(auditPage);
    } catch {
      if (generation === selectedGeneration.current)
        setError('Не удалось открыть запись, измерения и аудит из ядра.');
    }
  }
  async function moreMeasurements() {
    if (!selected || !measurements?.nextAfter) return;
    const generation = selectedGeneration.current;
    const request = ++measurementsGeneration.current;
    try {
      const page = await currentService.current.measurements(
        selected.testId,
        measurements.nextAfter,
        100,
      );
      if (generation === selectedGeneration.current && request === measurementsGeneration.current)
        setMeasurements(page);
    } catch {
      if (generation === selectedGeneration.current && request === measurementsGeneration.current)
        setError('Не удалось прочитать следующую страницу измерений.');
    }
  }
  async function moreAudit() {
    if (!selected || !audit?.nextAfter) return;
    const generation = selectedGeneration.current;
    const request = ++auditGeneration.current;
    try {
      const page = await currentService.current.audit(selected.testId, audit.nextAfter, 100);
      if (generation === selectedGeneration.current && request === auditGeneration.current)
        setAudit(page);
    } catch {
      if (generation === selectedGeneration.current && request === auditGeneration.current)
        setError('Не удалось прочитать следующую страницу аудита.');
    }
  }
  return (
    <section className="workpage">
      <h1>Испытания</h1>
      <button
        className="work-button work-button-primary"
        type="button"
        disabled={disabled}
        onClick={onNew}
      >
        {draftAvailable ? 'Продолжить подготовку' : 'Новое испытание'}
      </button>
      {error && <p role="alert">{error}</p>}
      {draftAvailable &&
        (discarding ? (
          <p>
            Несохранённый ввод будет удалён.{' '}
            <button
              type="button"
              onClick={() => {
                onDiscard();
                setDiscarding(false);
              }}
            >
              Удалить черновик
            </button>{' '}
            <button type="button" onClick={() => setDiscarding(false)}>
              Сохранить черновик
            </button>
          </p>
        ) : (
          <button type="button" onClick={() => setDiscarding(true)}>
            Отказаться от черновика
          </button>
        ))}
      {error && (
        <button
          type="button"
          onClick={() => {
            void load();
          }}
        >
          Повторить чтение списка
        </button>
      )}
      {!list && !error && (
        <p>{available ? 'Чтение истории…' : 'Ожидание связи для чтения истории…'}</p>
      )}
      {list && (
        <>
          <h2>История</h2>
          {list.items?.length ? (
            <ul>
              {list.items.map((item) => (
                <li key={item.testId}>
                  <button
                    type="button"
                    onClick={() => {
                      void open(item.testId);
                    }}
                  >
                    {item.parameters.valve} · {item.createdAt} · {outcomeLabel(item.outcome)}
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p>Записей нет.</p>
          )}
          {list.nextAfter && (
            <button
              type="button"
              onClick={() => {
                if (list.nextAfter) void load(list.nextAfter);
              }}
            >
              Следующая страница
            </button>
          )}
          <button
            type="button"
            onClick={() => {
              void load();
            }}
          >
            К началу списка
          </button>
        </>
      )}
      {selected && (
        <section>
          <h2>Сохранённая запись</h2>
          <p>
            {selected.parameters.valve} · {selected.testId}
          </p>
          <button
            type="button"
            disabled={disabled || draftAvailable}
            onClick={() => onCopy(selected.parameters)}
          >
            Новое по этим параметрам
          </button>
          {draftAvailable && <p>Продолжите текущий черновик перед созданием другого.</p>}
          <p>
            Исход: {outcomeLabel(selected.outcome)} · Качество данных:{' '}
            {qualityLabel(selected.dataQuality)} · Сохранение: {saveLabel(selected.saveStatus)}
          </p>
          <p>
            Площадь: {selected.parameters.areaM2} м² · Pmax: {selected.parameters.pmaxPa} Па ·
            Интервал: {selected.parameters.measurementSeconds} с
          </p>
          <h3>Измерения</h3>
          <ul>
            {measurements?.items?.map((item) => (
              <li key={item.sequence}>
                {item.channel}: {item.value ?? 'неизвестно'} {item.unit} · {item.quality} ·{' '}
                {item.measuredAt ?? 'время неизвестно'} · {item.source}
              </li>
            ))}
          </ul>
          {measurements?.nextAfter && (
            <button
              type="button"
              onClick={() => {
                void moreMeasurements();
              }}
            >
              Следующая страница измерений
            </button>
          )}
          <h3>Аудит</h3>
          <ul>
            {audit?.items?.map((item) => (
              <li key={item.sequence}>
                {item.occurredAt} · {item.action} · {item.outcome} · {item.operatorId}
                {item.commandId ? ` · команда ${item.commandId}` : ''}
                {item.actionId ? ` · действие ${item.actionId}` : ''}
                {item.reason ? ` · причина ${item.reason}` : ''}
              </li>
            ))}
          </ul>
          {audit?.nextAfter && (
            <button
              type="button"
              onClick={() => {
                void moreAudit();
              }}
            >
              Следующая страница аудита
            </button>
          )}
          <button
            type="button"
            onClick={() => {
              void open(selected.testId);
            }}
          >
            К началу измерений и аудита
          </button>
        </section>
      )}
    </section>
  );
}
