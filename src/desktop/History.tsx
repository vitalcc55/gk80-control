import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ResultScreen } from '../components/ResultScreen';
import { emptyHistoryFilters, type HistoryFilters, TestsScreen } from '../components/TestsScreen';
import type {
  ListTestsParams,
  TestList,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { TestRecordOutcome } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DemoService } from './demo-service';
import { outcomeLabel, saveLabel } from './display';
import { parametersForDisplay } from './presentation';
import { RecordDetails } from './RecordDetails';

function queryParameters(filters: HistoryFilters): ListTestsParams {
  const params: ListTestsParams = {};
  if (filters.query.trim()) params.q = filters.query.trim();
  // Calendar boundaries are local midnights; advancing the date also handles DST.
  if (filters.from) params.createdFrom = new Date(`${filters.from}T00:00:00`).toISOString();
  if (filters.through) {
    const end = new Date(`${filters.through}T00:00:00`);
    end.setDate(end.getDate() + 1);
    params.createdBefore = end.toISOString();
  }
  if (
    params.createdFrom &&
    params.createdBefore &&
    Date.parse(params.createdFrom) >= Date.parse(params.createdBefore)
  )
    throw new Error('Начало диапазона должно быть не позже конца.');
  switch (filters.outcome) {
    case 'completed':
      params.outcome = TestRecordOutcome.TestRecordOutcomeCompleted;
      break;
    case 'interrupted':
      params.outcome = TestRecordOutcome.TestRecordOutcomeInterrupted;
      break;
    case 'pending':
      params.outcome = TestRecordOutcome.TestRecordOutcomePending;
      break;
    case 'unknown':
      params.outcome = TestRecordOutcome.TestRecordOutcomeUnknown;
  }
  return params;
}

export function History({
  onNew,
  onCopy,
  onDiscard,
  disabled,
  available,
  draftAvailable,
  service,
  visible = true,
  onViewChange,
  resetVersion = 0,
  activeTestId,
}: {
  onNew: () => void;
  onCopy: (parameters: TestRecord['parameters']) => void;
  onDiscard: () => void;
  disabled: boolean;
  available: boolean;
  draftAvailable: boolean;
  service: DemoService;
  visible?: boolean;
  onViewChange?: (result: boolean) => void;
  resetVersion?: number;
  activeTestId?: string | undefined;
}) {
  const [list, setList] = useState<TestList | null>(null);
  const [selected, setSelected] = useState<TestRecord | null>(null);
  const [filters, setFilters] = useState(emptyHistoryFilters);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [opening, setOpening] = useState(false);
  const [discarding, setDiscarding] = useState(false);
  const listGeneration = useRef(0);
  const selectedGeneration = useRef(0);
  // Null is an explicitly rejected search, never a fallback to the previous query.
  const applied = useRef<ListTestsParams | null>({});
  const cursor = useRef<string | null>(null);
  const currentService = useRef(service);
  useLayoutEffect(() => {
    currentService.current = service;
  }, [service]);
  useEffect(
    () => () => {
      listGeneration.current++;
      selectedGeneration.current++;
    },
    [],
  );
  const load = useCallback(async (after: string | null = null) => {
    const params = applied.current;
    if (params === null) return;
    const generation = ++listGeneration.current;
    cursor.current = after;
    setList(null);
    setLoading(true);
    setError('');
    try {
      const page = await currentService.current.listTests({
        ...params,
        limit: 100,
        after,
      });
      if (generation === listGeneration.current) setList(page);
    } catch {
      if (generation === listGeneration.current)
        setError('Не удалось прочитать список испытаний из ядра.');
    } finally {
      if (generation === listGeneration.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    if (visible && available) void load(cursor.current);
    return () => {
      listGeneration.current++;
      selectedGeneration.current++;
      setLoading(false);
      setOpening(false);
    };
  }, [visible, available, load]);
  useEffect(() => {
    void resetVersion;
    selectedGeneration.current++;
    setSelected(null);
    setOpening(false);
  }, [resetVersion]);
  async function open(testId: string) {
    const generation = ++selectedGeneration.current;
    setError('');
    setOpening(true);
    try {
      const record = await currentService.current.test(testId);
      if (generation === selectedGeneration.current) {
        setSelected(record);
        onViewChange?.(true);
      }
    } catch {
      if (generation === selectedGeneration.current) setError('Не удалось открыть запись из ядра.');
    } finally {
      if (generation === selectedGeneration.current) setOpening(false);
    }
  }
  function search(value: HistoryFilters) {
    listGeneration.current++;
    selectedGeneration.current++;
    cursor.current = null;
    setOpening(false);
    setList(null);
    applied.current = null;
    try {
      applied.current = queryParameters(value);
      if (available) void load();
      else setError('Ожидание связи для чтения истории…');
    } catch (reason) {
      setLoading(false);
      setError(reason instanceof Error ? reason.message : 'Проверьте даты.');
    }
  }
  if (selected)
    return (
      <ResultScreen
        parameters={parametersForDisplay(selected)}
        record={selected}
        saveError={selected.saveStatus === 'failed'}
        interrupted={selected.outcome === 'interrupted'}
        active={disabled || draftAvailable}
        draftAvailable={false}
        onList={() => {
          setSelected(null);
          onViewChange?.(false);
        }}
        onNew={() => onCopy(selected.parameters)}
      >
        {draftAvailable && <p>Продолжите текущий черновик перед созданием другого.</p>}
        <RecordDetails
          key={selected.testId}
          record={selected}
          service={service}
          available={available}
        />
      </ResultScreen>
    );
  return (
    <TestsScreen
      rows={(list?.items ?? [])
        .filter((item) => item.testId !== activeTestId)
        .map((item) => ({
          id: item.testId,
          valve: item.parameters.valve,
          createdAt: item.createdAt,
          direction: 'Нагнетание',
          pressure: item.parameters.pmaxPa,
          actuator: false,
          outcome: outcomeLabel(item.outcome),
          saving: saveLabel(item.saveStatus),
        }))}
      filters={filters}
      onFilters={setFilters}
      onSearch={() => search(filters)}
      onReset={() => {
        setFilters(emptyHistoryFilters);
        search(emptyHistoryFilters);
      }}
      onOpen={(id) => void open(id)}
      onNew={onNew}
      disabled={disabled}
      draftAvailable={draftAvailable}
      loading={loading || opening}
      error={error || (!available ? 'Ожидание связи для чтения истории…' : '')}
      onRetry={() => {
        if (available) search(filters);
      }}
      onFirst={() => {
        if (available) void load();
      }}
      onNext={
        list?.nextAfter
          ? () => {
              if (list.nextAfter && available) void load(list.nextAfter);
            }
          : undefined
      }
    >
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
          <button className="table-action" type="button" onClick={() => setDiscarding(true)}>
            Отказаться от черновика
          </button>
        ))}
    </TestsScreen>
  );
}
