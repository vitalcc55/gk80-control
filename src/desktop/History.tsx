import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ResultScreen } from '../components/ResultScreen';
import {
  emptyHistoryFilters,
  type HistoryFilters,
  normalizeHistoryQuery,
  TestsScreen,
} from '../components/TestsScreen';
import type {
  ListTestsParams,
  TestList,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { TestRecordOutcome } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DemoService } from './demo-service';
import { outcomeLabel, qualityLabel, saveLabel } from './display';
import { parametersForDisplay, resultReadVersion } from './presentation';
import { RecordDetails } from './RecordDetails';

type RecordReadFailure = { text: string; testId: string };

function queryParameters(filters: HistoryFilters): ListTestsParams {
  const params: ListTestsParams = {};
  const query = normalizeHistoryQuery(filters.query);
  if (query) params.q = query;
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
  const [pageSize, setPageSize] = useState(20);
  const [pageNumber, setPageNumber] = useState(1);
  const requestedPage = useRef(1);
  const [totalChanged, setTotalChanged] = useState(false);
  const firstPageTotal = useRef<number | null>(null);
  const currentPageSize = useRef(20);
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
  const readTriggers = useRef({
    visible: false,
    available,
    resetVersion,
    completedKey: '',
    activeTestId,
  });
  const completedId = completedRecord?.testId;
  const selectedId = selected?.testId;
  const selectedKey = selected ? resultReadVersion(selected) : '';
  const currentService = useRef(service);
  const currentActive = useRef({ id: activeTestId, onCurrent });
  useLayoutEffect(() => {
    currentService.current = service;
    currentActive.current = { id: activeTestId, onCurrent };
  }, [service, activeTestId, onCurrent]);
  const cancelSelected = useCallback((clear: boolean) => {
    selectedGeneration.current++;
    selectedRefresh.current = '';
    setOpening(false);
    setRecordFailure(null);
    if (clear) setSelected(null);
  }, []);
  useLayoutEffect(() => {
    if (
      visible &&
      readTriggers.current.resetVersion === resetVersion &&
      selectedId &&
      selectedId === activeTestId &&
      currentActive.current.onCurrent
    ) {
      cancelSelected(true);
      currentActive.current.onCurrent();
    }
  }, [visible, selectedId, activeTestId, resetVersion, cancelSelected]);
  useEffect(
    () => () => {
      listGeneration.current++;
      selectedGeneration.current++;
    },
    [],
  );
  const load = useCallback(
    async (after: string | null = null, pageNumber = requestedPage.current) => {
      const params = applied.current;
      if (params === null) return;
      needsRead.current = false;
      const generation = ++listGeneration.current;
      cursor.current = after;
      requestedPage.current = pageNumber;
      setList(null);
      setLoading(true);
      setListError('');
      try {
        const page = await currentService.current.listTests({
          ...params,
          limit: currentPageSize.current,
          after,
        });
        if (generation === listGeneration.current) {
          if (requestedPage.current === 1) {
            firstPageTotal.current = page.total;
            setTotalChanged(false);
          } else if (page.total === null || typeof page.total === 'number') {
            setTotalChanged(
              (previous) =>
                previous ||
                page.total === null ||
                typeof firstPageTotal.current !== 'number' ||
                page.total !== firstPageTotal.current,
            );
          }
          setList(page);
          setPageNumber(requestedPage.current);
        }
      } catch {
        if (generation === listGeneration.current)
          setListError('Не удалось прочитать список испытаний из ядра.');
      } finally {
        if (generation === listGeneration.current) setLoading(false);
      }
    },
    [],
  );
  useLayoutEffect(() => {
    const previous = readTriggers.current;
    const reset = previous.resetVersion !== resetVersion;
    if (previous.available && !available) {
      listGeneration.current++;
      if (loading) needsRead.current = true;
      setLoading(false);
    }
    if (previous.activeTestId !== activeTestId) {
      listGeneration.current++;
      setList(null);
      setLoading(false);
    }
    if (
      (visible && !previous.visible) ||
      reset ||
      previous.completedKey !== completedKey ||
      previous.activeTestId !== activeTestId
    )
      needsRead.current = true;
    readTriggers.current = { visible, available, resetVersion, completedKey, activeTestId };
    if ((!visible && previous.visible) || reset) {
      cancelSelected(reset);
    }
    if (visible && available && needsRead.current) void load(cursor.current);
  }, [visible, available, resetVersion, completedKey, activeTestId, load, cancelSelected, loading]);
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
        if (currentActive.current.id === testId && currentActive.current.onCurrent) {
          currentActive.current.onCurrent();
        } else {
          setSelected(record);
          onViewChange?.(true);
        }
      }
    } catch {
      if (generation === selectedGeneration.current) {
        if (currentActive.current.id === testId && currentActive.current.onCurrent)
          currentActive.current.onCurrent();
        else setRecordFailure({ testId, text: 'Не удалось открыть запись из ядра.' });
      }
    } finally {
      if (generation === selectedGeneration.current) setOpening(false);
    }
  }
  function search(value: HistoryFilters) {
    listGeneration.current++;
    cancelSelected(false);
    cursor.current = null;
    requestedPage.current = 1;
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
        {completedRecord &&
          selected.testId === completedRecord.testId &&
          selectedKey !== completedKey && (
            <p role="status">
              В истории сохранена другая версия. По последнему снимку:{' '}
              {outcomeLabel(completedRecord.outcome)}; данные:{' '}
              {qualityLabel(completedRecord.dataQuality)}; сохранение:{' '}
              {saveLabel(completedRecord.saveStatus)}.
            </p>
          )}
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
        saving:
          completedRecord &&
          item.testId === completedRecord.testId &&
          resultReadVersion(item) !== completedKey
            ? `${saveLabel(item.saveStatus)}. По последнему снимку: ${outcomeLabel(completedRecord.outcome)}; данные: ${qualityLabel(completedRecord.dataQuality)}; сохранение: ${saveLabel(completedRecord.saveStatus)}.`
            : saveLabel(item.saveStatus),
        active: item.testId === activeTestId,
      }))}
      filters={filters}
      pageSize={pageSize}
      pageNumber={pageNumber}
      total={list?.total}
      pageChanged={
        !!list &&
        (totalChanged ||
          (list.total !== null &&
            list.total !== undefined &&
            ((list.items?.length ?? 0) !==
              Math.min(pageSize, Math.max(0, list.total - (pageNumber - 1) * pageSize)) ||
              (!list.nextAfter && pageNumber < Math.ceil(list.total / pageSize)))))
      }
      onRefreshCount={available ? () => void load(cursor.current) : undefined}
      onPageSize={(value) => {
        currentPageSize.current = value;
        setPageSize(value);
        listGeneration.current++;
        cancelSelected(false);
        cursor.current = null;
        requestedPage.current = 1;
        needsRead.current = true;
        setList(null);
        setListError('');
        setLoading(false);
        if (available && applied.current !== null) void load();
      }}
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
        if (available) void load(null, 1);
      }}
      onNext={
        list?.nextAfter
          ? () => {
              if (list.nextAfter && available) void load(list.nextAfter, pageNumber + 1);
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
