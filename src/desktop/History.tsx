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
import { parametersForDisplay, resultReadVersion } from './presentation';
import { RecordDetails } from './RecordDetails';

type RecordReadFailure = { text: string; testId: string };

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
  completedRecord = null,
  onCurrent,
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
  completedRecord?: TestRecord | null;
  onCurrent?: () => void;
}) {
  const [list, setList] = useState<TestList | null>(null);
  const [selected, setSelected] = useState<TestRecord | null>(null);
  const [filters, setFilters] = useState(emptyHistoryFilters);
  const [validationError, setValidationError] = useState('');
  const [listError, setListError] = useState('');
  const [recordFailure, setRecordFailure] = useState<RecordReadFailure | null>(null);
  const [loading, setLoading] = useState(false);
  const [opening, setOpening] = useState(false);
  const [discarding, setDiscarding] = useState(false);
  const listGeneration = useRef(0);
  const selectedGeneration = useRef(0);
  // Null is an explicitly rejected search, never a fallback to the previous query.
  const applied = useRef<ListTestsParams | null>({});
  const cursor = useRef<string | null>(null);
  const needsRead = useRef(true);
  const selectedRefresh = useRef('');
  const completedKey = completedRecord ? resultReadVersion(completedRecord) : '';
  const readTriggers = useRef({ visible: false, resetVersion, completedKey: '' });
  const completedId = completedRecord?.testId;
  const selectedId = selected?.testId;
  const selectedKey = selected ? resultReadVersion(selected) : '';
  const currentService = useRef(service);
  useLayoutEffect(() => {
    currentService.current = service;
  }, [service]);
  const cancelSelected = useCallback((clear: boolean) => {
    selectedGeneration.current++;
    selectedRefresh.current = '';
    setOpening(false);
    setRecordFailure(null);
    if (clear) setSelected(null);
  }, []);
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
    needsRead.current = false;
    const generation = ++listGeneration.current;
    cursor.current = after;
    setList(null);
    setLoading(true);
    setListError('');
    try {
      const page = await currentService.current.listTests({
        ...params,
        limit: 100,
        after,
      });
      if (generation === listGeneration.current) setList(page);
    } catch {
      if (generation === listGeneration.current)
        setListError('Не удалось прочитать список испытаний из ядра.');
    } finally {
      if (generation === listGeneration.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    const previous = readTriggers.current;
    const reset = previous.resetVersion !== resetVersion;
    if ((visible && !previous.visible) || reset || previous.completedKey !== completedKey)
      needsRead.current = true;
    readTriggers.current = { visible, resetVersion, completedKey };
    if ((!visible && previous.visible) || reset) {
      cancelSelected(reset);
    }
    if (visible && available && needsRead.current) void load(cursor.current);
  }, [visible, available, resetVersion, completedKey, load, cancelSelected]);
  useEffect(() => {
    if (
      !visible ||
      !available ||
      !selectedId ||
      selectedId !== completedId ||
      selectedKey === completedKey
    )
      return;
    const key = `${selectedId}:${completedKey}`;
    if (selectedRefresh.current === key) return;
    selectedRefresh.current = key;
    const generation = ++selectedGeneration.current;
    setRecordFailure(null);
    void currentService.current.test(selectedId).then(
      (value) => {
        if (generation === selectedGeneration.current) setSelected(value);
      },
      () => {
        if (generation === selectedGeneration.current)
          setRecordFailure({
            testId: selectedId,
            text: 'Не удалось обновить запись после сохранения.',
          });
      },
    );
  }, [visible, available, selectedId, selectedKey, completedId, completedKey]);
  async function open(testId: string) {
    const generation = ++selectedGeneration.current;
    setRecordFailure(null);
    setOpening(true);
    try {
      const record = await currentService.current.test(testId);
      if (generation === selectedGeneration.current) {
        setSelected(record);
        onViewChange?.(true);
      }
    } catch {
      if (generation === selectedGeneration.current)
        setRecordFailure({ testId, text: 'Не удалось открыть запись из ядра.' });
    } finally {
      if (generation === selectedGeneration.current) setOpening(false);
    }
  }
  function search(value: HistoryFilters) {
    listGeneration.current++;
    cancelSelected(false);
    cursor.current = null;
    setOpening(false);
    setLoading(false);
    setList(null);
    applied.current = null;
    needsRead.current = true;
    setValidationError('');
    setListError('');
    try {
      applied.current = queryParameters(value);
      if (available) void load();
    } catch (reason) {
      setLoading(false);
      setValidationError(reason instanceof Error ? reason.message : 'Проверьте даты.');
    }
  }
  const retry = recordFailure
    ? () => {
        if (available) void open(recordFailure.testId);
      }
    : listError
      ? () => {
          if (available) void load(cursor.current);
        }
      : undefined;
  const retryLabel = recordFailure ? 'Повторить открытие записи' : 'Повторить чтение списка';
  if (selected)
    return (
      <ResultScreen
        parameters={parametersForDisplay(selected)}
        record={selected}
        hidden={!visible}
        saveError={selected.saveStatus === 'failed'}
        interrupted={selected.outcome === 'interrupted'}
        active={disabled || draftAvailable}
        draftAvailable={false}
        onList={() => {
          cancelSelected(true);
          onViewChange?.(false);
        }}
        onNew={() => onCopy(selected.parameters)}
      >
        {visible && recordFailure && (
          <div role="alert">
            <p>{recordFailure.text}</p>
            {retry && (
              <button type="button" onClick={retry}>
                {retryLabel}
              </button>
            )}
          </div>
        )}
        {draftAvailable && <p>Продолжите текущий черновик перед созданием другого.</p>}
        <RecordDetails
          key={selected.testId}
          record={selected}
          service={service}
          available={available}
          visible={visible}
        />
      </ResultScreen>
    );
  if (!visible) return null;
  return (
    <TestsScreen
      rows={(list?.items ?? []).map((item) => ({
        id: item.testId,
        valve: item.parameters.valve,
        createdAt: item.createdAt,
        direction: 'Нагнетание',
        pressure: item.parameters.pmaxPa,
        actuator: false,
        outcome: outcomeLabel(item.outcome),
        saving: saveLabel(item.saveStatus),
        active: item.testId === activeTestId,
      }))}
      filters={filters}
      filtered={!!(applied.current && Object.keys(applied.current).length)}
      onFilters={setFilters}
      onSearch={() => search(filters)}
      onReset={() => {
        setFilters(emptyHistoryFilters);
        search(emptyHistoryFilters);
      }}
      onOpen={(id) => {
        if (id === activeTestId && onCurrent) onCurrent();
        else void open(id);
      }}
      onNew={onNew}
      disabled={disabled}
      draftAvailable={draftAvailable}
      loading={loading || opening}
      error={
        validationError ||
        recordFailure?.text ||
        listError ||
        (!available && !list ? 'Ожидание связи для чтения истории…' : '')
      }
      onRetry={retry}
      retryLabel={retryLabel}
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
              Оставить черновик
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
