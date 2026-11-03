import {
  type Dispatch,
  type SetStateAction,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import {
  ActiveParametersScreen,
  draftFrom,
  ParametersScreen,
  ReadinessScreen,
  ResultScreen,
} from '../components/OtherScreens';
import {
  type CommandDispatch,
  type CommandStatus,
  type Readiness,
  type Snapshot,
  TestParametersDirection,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { TestParameters as FormParameters } from '../model';
import type { SessionBridge } from './bridge';
import { beginCall, countCalls, endCall, totalCalls, useCallRevision } from './call-limits';
import {
  acknowledgeStored,
  type CommandFailure,
  commandLookupKey,
  commandsBlock,
  confirmedCommandStatus,
  hasUnstoredStop,
  memoryFor,
  mergePending,
  mergeStatus,
  type Pending,
  pendingKey,
  pruneStatuses,
  readLegacyPending,
  readStoredPending,
  restoredMemory,
  restorePending,
  samePending,
  stopAttemptNoticeKey,
  stopLookupNoticeKey,
  stopNoticeKey,
  terminalStatus,
  validDispatchEnvelope,
  validExactEnvelope,
} from './command-attempt-state';
import { CommandConflictError, type DemoService } from './demo-service';
import { History } from './History';
import { LiveRun } from './LiveRun';
import { observeReadiness } from './readiness-observation';
import { SessionStatus } from './SessionStatus';
import { type SessionObservation, sessionObservation } from './session-observation';
import { useSessionObservation } from './use-session-observation';

const empty: FormParameters = {
  id: '',
  valve: '',
  area: 0.012,
  pmax: 10000,
  direction: 'pressure',
  actuator: false,
  voltage: 24,
  openSeconds: 3,
  closeSeconds: 3,
  measurementSeconds: 60,
};
function newDraft() {
  return { ...draftFrom(empty), area: '', pmax: '' };
}
const legacyCommandLabels: Record<Pending['kind'], string> = {
  create: 'создание испытания',
  start: 'запуск испытания',
  confirm: 'подтверждение действия',
  stop: 'остановка испытания',
};

function commandStatusLabel(status: CommandStatus['status'] | undefined): string {
  switch (status) {
    case 'accepted':
      return 'принята; завершение команды не подтверждено';
    case 'completed':
      return 'выполнена';
    case 'rejected':
      return 'отклонена';
    case 'failed':
      return 'завершилась с ошибкой';
    default:
      return 'исход неизвестен';
  }
}

function stopPendingMessage(record: Snapshot['activeTest']): string {
  return record?.stage === 'finished' && record.outcome === 'interrupted'
    ? 'Испытание прервано по состоянию ядра; исход запроса остановки ещё не подтверждён.'
    : 'Остановка запрошена, её завершение ещё не подтверждено.';
}

function stopOutcomeMessage(status: CommandStatus, record: Snapshot['activeTest']): string {
  return status.status === 'completed'
    ? 'Остановка подтверждена ядром.'
    : status.status === 'rejected' || status.status === 'failed'
      ? `Остановка не выполнена или отклонена${status.reason ? `: ${status.reason}` : '.'} Безопасное давление не подтверждено.`
      : stopPendingMessage(record);
}

type LiveAppProps = {
  session: SessionBridge;
  service: DemoService;
  hostRunId: string;
  backendRunId: string;
  databaseId: string;
  diagnosticsDegraded?: boolean;
  pendingStorage?: Storage;
  suspended?: boolean;
};

const observationDatabases = new WeakMap<
  SessionObservation,
  { database: string; minimum: number }
>();

function bindObservationDatabase(props: LiveAppProps) {
  const observer = sessionObservation(props.hostRunId, props.backendRunId);
  const previous = observationDatabases.get(observer);
  const minimum =
    previous?.database === props.databaseId
      ? previous.minimum
      : previous
        ? observer.getSnapshot().observationVersion + 1
        : 0;
  observationDatabases.set(observer, { database: props.databaseId, minimum });
  return {
    host: props.hostRunId,
    backend: props.backendRunId,
    database: props.databaseId,
    minimum,
  };
}

export function LiveApp(props: LiveAppProps) {
  const [binding, setBinding] = useState(() => bindObservationDatabase(props));
  let currentBinding = binding;
  if (
    binding.host !== props.hostRunId ||
    binding.backend !== props.backendRunId ||
    binding.database !== props.databaseId
  ) {
    currentBinding = bindObservationDatabase(props);
    setBinding(currentBinding);
  }
  const [draft, setDraft] = useState(newDraft);
  const [draftAvailable, setDraftAvailable] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  if (props.suspended) return null;
  return (
    <ActiveLiveApp
      key={`${props.hostRunId}:${props.backendRunId}:${props.databaseId}`}
      {...props}
      minimumObservation={currentBinding.minimum}
      draft={draft}
      setDraft={setDraft}
      draftAvailable={draftAvailable}
      setDraftAvailable={setDraftAvailable}
      showForm={showForm}
      setShowForm={setShowForm}
      showHistory={showHistory}
      setShowHistory={setShowHistory}
    />
  );
}

function ActiveLiveApp({
  session,
  service,
  hostRunId,
  backendRunId,
  databaseId,
  diagnosticsDegraded = false,
  pendingStorage,
  minimumObservation,
  draft,
  setDraft,
  draftAvailable,
  setDraftAvailable,
  showForm,
  setShowForm,
  showHistory,
  setShowHistory,
}: LiveAppProps & {
  minimumObservation: number;
  draft: ReturnType<typeof newDraft>;
  setDraft: Dispatch<SetStateAction<ReturnType<typeof newDraft>>>;
  draftAvailable: boolean;
  setDraftAvailable: Dispatch<SetStateAction<boolean>>;
  showForm: boolean;
  setShowForm: Dispatch<SetStateAction<boolean>>;
  showHistory: boolean;
  setShowHistory: Dispatch<SetStateAction<boolean>>;
}) {
  const { observer, projection } = useSessionObservation(session, hostRunId, backendRunId);
  const acceptedProjection = projection.observationVersion >= minimumObservation;
  const connection = acceptedProjection ? projection.connection : null;
  const lastSnapshot = acceptedProjection ? projection.lastSnapshot : null;
  const { gapVersion } = projection;
  const [showSavedParameters, setShowSavedParameters] = useState(false);
  const [readiness, setReadiness] = useState<Readiness | null>(null);
  const [readinessConfirmed, setReadinessConfirmed] = useState(false);
  const storageKey = `gk80.demo.pending-command.v1:${databaseId}`;
  const [memory] = useState(() => memoryFor(databaseId));
  const [restored] = useState(() => restorePending(storageKey, memory, pendingStorage));
  const [legacy, setLegacy] = useState(readLegacyPending);
  const [legacyReviewed, setLegacyReviewed] = useState(false);
  const legacyBlocks =
    legacy.unreadable ||
    (!legacyReviewed && legacy.items.some((item) => !item.retired && item.kind !== 'stop'));
  const [storageUnconfirmed, setStorageUnconfirmed] = useState(
    restored === null || memory.storageUnknown,
  );
  const [pending, setPending] = useState<Pending[]>(
    restored ?? (memory.volatile ? restoredMemory(memory.volatile) : []),
  );
  const pendingRef = useRef(pending);
  const [message, setMessage] = useState('');
  const [commandFailure, setCommandFailure] = useState<CommandFailure | null>(null);
  const [latestNotice, setLatestNotice] = useState<'message' | 'commandFailure'>('message');
  const commandMessageRegion = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if ((latestNotice === 'message' ? message : commandFailure) && commandMessageRegion.current)
      commandMessageRegion.current.scrollTop = 0;
  }, [message, commandFailure, latestNotice]);
  const showMessage = useCallback((text: string) => {
    setMessage(text);
    setLatestNotice('message');
  }, []);
  const showCommandFailure = useCallback((failure: CommandFailure) => {
    setCommandFailure(failure);
    setLatestNotice('commandFailure');
  }, []);
  const [storageMessage, setStorageMessage] = useState('');
  const [commandMessages, setCommandMessages] = useState<Map<string, { id: string; text: string }>>(
    () => new Map(),
  );
  const setCommandMessage = useCallback((id: string, text: string, key = id) => {
    setCommandMessages((current) => {
      if ((current.get(key)?.text ?? '') === text) return current;
      const next = new Map(current);
      if (text) next.set(key, { id, text });
      else next.delete(key);
      return next;
    });
  }, []);
  const unknownMessage = useCallback(
    (id: string, key = id) => {
      setCommandMessage(
        id,
        'Исход команды пока неизвестен. Автоматический повтор не выполняется.',
        key,
      );
    },
    [setCommandMessage],
  );
  const [stopMessage, setStopMessageValue] = useState('');
  const stopMessageRegion = useRef<HTMLDivElement>(null);
  const stopMessageValue = useRef('');
  const setStopMessage = useCallback((value: string) => {
    if (value !== stopMessageValue.current && stopMessageRegion.current)
      stopMessageRegion.current.scrollTop = 0;
    stopMessageValue.current = value;
    setStopMessageValue(value);
  }, []);
  const [volatileStopTracking, setVolatileStopTracking] = useState(() =>
    memory.volatile ? hasUnstoredStop(memory.volatile, storageKey, pendingStorage) : false,
  );
  const lastStopId = useRef<string | null>(null);
  useCallRevision();
  const [stopRequestedFor, setStopRequestedFor] = useState<string | null>(null);
  const stopStatuses = useRef(new Map<string, CommandStatus>());
  const lookupIndex = useRef(0);
  const lifecycle = useRef(0);
  useEffect(() => {
    lifecycle.current += 1;
    return () => {
      lifecycle.current += 1;
    };
  }, []);
  const snapshot = connection?.view.snapshot ?? lastSnapshot;
  const record = snapshot?.activeTest ?? null;
  const activeId = record?.testId;
  const stage = record?.stage;
  const revision = snapshot?.revision;
  const currentTest = useRef<string | undefined>(activeId);
  currentTest.current = activeId;
  const currentRecord = useRef(record);
  currentRecord.current = record;
  const stopRequested = !!record && stopRequestedFor === record.testId;
  const stopCount = record ? countCalls('stop', record.testId) : 0;
  const totalStops = totalCalls('stop');
  const fresh =
    !!connection &&
    !!snapshot &&
    Number.isSafeInteger(snapshot.revision) &&
    Number.isSafeInteger(snapshot.lastEventSequence);
  const blocking =
    legacyBlocks ||
    storageUnconfirmed ||
    pending.length >= 64 ||
    totalStops >= 63 ||
    commandsBlock(pending, record, snapshot);

  useEffect(() => {
    setReadinessConfirmed(false);
    if (fresh && stage === 'preparing' && activeId && revision !== undefined) {
      return observeReadiness(service, hostRunId, backendRunId, activeId, revision, (value) => {
        if (value) {
          setReadiness(value);
          setReadinessConfirmed(true);
        } else {
          setReadinessConfirmed(false);
          showMessage('Не удалось подтвердить готовность.');
        }
      });
    }
    return undefined;
  }, [fresh, activeId, stage, revision, service, hostRunId, backendRunId, showMessage]);

  const savePending = useCallback(
    (values: Pending[], allowVolatile = false, remove?: Pending): boolean => {
      let recovered = values;
      try {
        for (const item of memory.retained) {
          if (item.observationVersion) continue;
          const observationVersion = crypto.randomUUID();
          memory.retained = memory.retained.map((entry) =>
            samePending(entry, item) ? { ...entry, observationVersion } : entry,
          );
          values = values.map((entry) =>
            samePending(entry, item) ? { ...entry, observationVersion } : entry,
          );
          recovered = values;
        }
        if (memory.storageUnknown) {
          const stored = readStoredPending(storageKey, pendingStorage);
          if (!stored) throw new Error('pending storage not restored');
          acknowledgeStored(memory, stored);
          values = mergePending(stored, values, memory.baseline, memory.retained);
          recovered = values;
          memory.baseline = stored;
        }
        if (remove && !memory.removals.some((item) => samePending(item, remove)))
          memory.removals.push(remove);
        values = values.filter(
          (item) => !memory.removals.some((entry) => samePending(entry, item)),
        );
        if (values.length > 65) throw new Error('pending list full');
        (pendingStorage ?? sessionStorage).setItem(
          storageKey,
          JSON.stringify(
            values.map(
              ({
                id,
                kind,
                testId,
                retired,
                receiptRequired,
                legacyReviewed,
                originalJSON,
                actionId,
                observationVersion,
              }) => ({
                id,
                kind,
                testId,
                retired,
                receiptRequired,
                legacyReviewed,
                ...(observationVersion ? { observationVersion } : {}),
                ...(originalJSON === null ? {} : { schema: 2, originalJSON, actionId }),
              }),
            ),
          ),
        );
        pendingRef.current = values;
        setPending(values);
        memory.volatile = null;
        memory.baseline = values;
        memory.removals = [];
        memory.retained = [];
        pruneStatuses(memory, values);
        memory.storageUnknown = false;
        setStorageUnconfirmed(false);
        setVolatileStopTracking(false);
        setStorageMessage('');
        return true;
      } catch {
        memory.storageUnknown = true;
        setStorageUnconfirmed(true);
        if (!allowVolatile && !remove) {
          recovered = pendingRef.current.map((item) => {
            const retained = memory.retained.find((entry) => samePending(entry, item));
            return retained?.observationVersion
              ? { ...item, observationVersion: retained.observationVersion }
              : item;
          });
        }
        memory.volatile = recovered;
        pendingRef.current = recovered;
        setPending(recovered);
        setVolatileStopTracking(hasUnstoredStop(recovered, storageKey, pendingStorage));
        setStorageMessage(
          'Не удалось обновить список команд в интерфейсе; проверьте их состояние перед продолжением.',
        );
        return false;
      }
    },
    [memory, pendingStorage, storageKey],
  );
  const forget = useCallback(
    (item: Pending) => {
      if (
        pendingRef.current.some((current) => samePending(current, item)) &&
        !savePending(pendingRef.current, false, item)
      )
        return;
      setCommandMessage(
        item.id,
        '',
        item.kind === 'stop' ? stopNoticeKey(item.id, item.testId, item.originalJSON) : item.id,
      );
      if (item.kind === 'stop')
        setCommandMessage(
          item.id,
          '',
          stopLookupNoticeKey(item.id, item.testId, item.originalJSON),
        );
      if (item.kind === 'stop')
        setCommandMessage(
          item.id,
          '',
          stopAttemptNoticeKey(item.id, item.testId, item.originalJSON),
        );
    },
    [savePending, setCommandMessage],
  );
  const updateTrackedStatus = useCallback(
    (id: string, testId: string, originalJSON: string, status: CommandStatus) => {
      const current = pendingRef.current.find(
        (item) =>
          item.id === id &&
          item.kind === 'stop' &&
          item.testId === testId &&
          item.originalJSON === originalJSON,
      );
      if (!current) return;
      const merged = mergeStatus(current.status, status);
      memory.statuses.set(pendingKey(current), merged);
      if (JSON.stringify(current.status) === JSON.stringify(merged)) return;
      pendingRef.current = pendingRef.current.map((item) =>
        samePending(item, current) ? { ...item, status: merged } : item,
      );
      if (memory.volatile) memory.volatile = pendingRef.current;
      setPending(pendingRef.current);
    },
    [memory],
  );
  const lookup = useCallback(
    async (item: Pending, manual = false) => {
      if (item.originalJSON === null) {
        if (manual)
          setCommandMessage(
            item.id,
            'Исход этой команды не подтверждён; требуется точная сверка её содержимого.',
            item.kind === 'stop' ? stopNoticeKey(item.id, item.testId, item.originalJSON) : item.id,
          );
        return;
      }
      const lookupKey = commandLookupKey(hostRunId, backendRunId, databaseId, item.id);
      const lookupKind = item.kind === 'stop' ? 'stopLookup' : 'lookup';
      if (!beginCall(lookupKind, lookupKey, manual ? 2 : 1)) {
        if (manual) showMessage('Ожидаем завершения предыдущих проверок состояния команды.');
        return;
      }
      const generation = lifecycle.current;
      try {
        let status: CommandStatus;
        {
          const noticeKey =
            item.kind === 'stop' ? stopNoticeKey(item.id, item.testId, item.originalJSON) : item.id;
          const result = await service.lookupCommandExact(
            databaseId,
            item.kind,
            item.testId ?? '',
            item.actionId ?? '',
            item.id,
            item.originalJSON,
          );
          if (
            lifecycle.current !== generation ||
            !pendingRef.current.some((current) => samePending(current, item))
          )
            return;
          const current = pendingRef.current.find((pendingItem) => samePending(pendingItem, item));
          const alreadyConfirmed = current?.status && current.status.status !== 'unknown';
          if (!validExactEnvelope(result)) {
            if (!alreadyConfirmed) unknownMessage(item.id, noticeKey);
            return;
          }
          if (result.outcome === 'conflict' && result.command === null) {
            if (item.kind === 'stop') {
              setCommandMessage(
                item.id,
                'Сохранённая запись принадлежит другой команде; точная сверка остановки невозможна.',
                stopLookupNoticeKey(item.id, item.testId, item.originalJSON),
              );
            } else if (!alreadyConfirmed) {
              forget(item);
              showCommandFailure({
                id: item.id,
                kind: item.kind,
                testId: item.testId,
                text: 'Идентификатор принадлежит другой команде. Эта попытка отклонена.',
              });
            }
            return;
          }
          if (
            result.outcome !== 'match' ||
            !confirmedCommandStatus(result.command, item.id, item.testId)
          ) {
            if (!alreadyConfirmed) unknownMessage(item.id, noticeKey);
            return;
          }
          status = result.command;
          if (item.kind === 'stop')
            setCommandMessage(
              item.id,
              '',
              stopLookupNoticeKey(item.id, item.testId, item.originalJSON),
            );
        }
        if (lifecycle.current !== generation || status.commandId !== item.id) return;
        const existing = pendingRef.current.find((current) => samePending(current, item));
        if (!existing || existing.originalJSON === null) return;
        let merged = mergeStatus(existing.status, status);
        if (existing.receiptRequired && status.status !== 'unknown') {
          savePending(
            pendingRef.current.map((current) =>
              samePending(current, item) ? { ...current, receiptRequired: false } : current,
            ),
            true,
          );
        }
        if (item.kind === 'stop') {
          const stopKey = stopNoticeKey(item.id, item.testId, item.originalJSON);
          const identity = pendingKey(item);
          const stopStatus = mergeStatus(stopStatuses.current.get(identity) ?? null, merged);
          stopStatuses.current.set(identity, stopStatus);
          merged = stopStatus;
          if (terminalStatus(stopStatus))
            setCommandMessage(item.id, '', stopNoticeKey(item.id, item.testId, item.originalJSON));
          if (lastStopId.current === stopKey) {
            if (stopStatus.status === 'completed') setStopMessage('Остановка подтверждена ядром.');
            else if (stopStatus.status === 'failed' || stopStatus.status === 'rejected')
              setStopMessage(
                'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.',
              );
          }
        }
        memory.statuses.set(pendingKey(item), merged);
        if (JSON.stringify(existing.status) !== JSON.stringify(merged)) {
          pendingRef.current = pendingRef.current.map((current) =>
            samePending(current, item) ? { ...current, status: merged } : current,
          );
          if (memory.volatile) memory.volatile = pendingRef.current;
          setPending(pendingRef.current);
        }
        if (item.kind !== 'stop' && merged.status === 'completed')
          setCommandMessage(item.id, 'Действие подтверждено ядром; ожидаем состояние.');
        else if (item.kind !== 'stop' && merged.status === 'accepted')
          setCommandMessage(item.id, 'Команда принята или её исход ещё не подтверждён.');
        if (merged.status === 'rejected' || merged.status === 'failed') {
          if (item.kind === 'stop') {
            const failure =
              'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.';
            if (
              lastStopId.current === null ||
              lastStopId.current === stopNoticeKey(item.id, item.testId, item.originalJSON)
            ) {
              lastStopId.current = stopNoticeKey(item.id, item.testId, item.originalJSON);
              setStopRequestedFor(item.testId);
              setStopMessage(failure);
            }
            savePending(
              pendingRef.current.map((current) =>
                samePending(current, item) ? { ...current, retired: true } : current,
              ),
              true,
            );
          } else {
            if (merged.reason === 'canceled_by_stop')
              showMessage('Запуск отменён при остановке испытания до подтверждения оператора.');
            else
              showCommandFailure({
                id: item.id,
                kind: item.kind,
                testId: item.testId,
                text: 'Команда отклонена или не выполнена.',
              });
            forget(item);
          }
        }
      } catch {
        const current = pendingRef.current.find((pendingItem) => samePending(pendingItem, item));
        const knownStop =
          item.kind === 'stop' ? (stopStatuses.current.get(pendingKey(item)) ?? null) : null;
        if (
          lifecycle.current === generation &&
          current &&
          (item.kind === 'stop'
            ? !terminalStatus(current.status)
            : !current.status || current.status.status === 'unknown') &&
          !terminalStatus(knownStop)
        )
          unknownMessage(
            item.id,
            item.kind === 'stop' ? stopNoticeKey(item.id, item.testId, item.originalJSON) : item.id,
          );
      } finally {
        endCall(lookupKind, lookupKey);
      }
    },
    [
      backendRunId,
      databaseId,
      forget,
      hostRunId,
      memory,
      savePending,
      setCommandMessage,
      setStopMessage,
      service,
      showCommandFailure,
      showMessage,
      unknownMessage,
    ],
  );
  useEffect(() => {
    if (!fresh || storageUnconfirmed || (!pending.length && !pendingRef.current.length)) return;
    const tracked = pendingRef.current;
    if (!tracked.length) return;
    for (const item of tracked) {
      const expected = item.status?.testId ?? item.testId;
      if (
        !item.receiptRequired &&
        item.status?.status === 'completed' &&
        record &&
        expected === record.testId &&
        (item.kind === 'create' || record.stage !== 'preparing') &&
        (item.kind !== 'stop' || record.stage === 'finished') &&
        (item.kind !== 'confirm' || !snapshot?.pendingAction)
      ) {
        if (item.kind === 'create') {
          setShowForm(false);
          setDraft(newDraft());
          setDraftAvailable(false);
        }
        forget(item);
        return;
      }
    }
    if (
      tracked.some(
        (item) =>
          item.kind === 'stop' &&
          !item.retired &&
          (item.status?.status === 'failed' || item.status?.status === 'rejected'),
      )
    ) {
      savePending(
        tracked.map((item) =>
          item.kind === 'stop' &&
          (item.status?.status === 'failed' || item.status?.status === 'rejected')
            ? { ...item, retired: true }
            : item,
        ),
        true,
      );
      return;
    }
    if (
      record?.stage === 'finished' &&
      tracked.some(
        (item) => !item.retired && !item.receiptRequired && item.testId === record.testId,
      )
    ) {
      savePending(
        tracked.map((item) =>
          item.testId === record.testId && !item.receiptRequired
            ? { ...item, retired: true }
            : item,
        ),
        true,
      );
      return;
    }
    const unresolved = tracked.filter(
      (item) =>
        item.originalJSON !== null &&
        (item.receiptRequired ||
          !terminalStatus(item.status) ||
          (item.kind === 'create' && item.status?.status === 'completed' && !item.status.testId)),
    );
    const item = unresolved[lookupIndex.current++ % unresolved.length];
    if (item) void lookup(item);
  }, [
    pending,
    storageUnconfirmed,
    fresh,
    record,
    snapshot,
    lookup,
    forget,
    savePending,
    setShowForm,
    setDraft,
    setDraftAvailable,
  ]);

  async function submit(
    kind: Exclude<Pending['kind'], 'stop'>,
    testId: string | null,
    actionId: string | null,
    prepare: (id: string) => { originalJSON: string; send: () => Promise<CommandDispatch> },
  ) {
    if (
      !fresh ||
      !snapshot ||
      !connection ||
      observer.getSnapshotFor(session).connection !== connection ||
      legacyBlocks ||
      storageUnconfirmed ||
      totalCalls('stop') >= 63 ||
      pendingRef.current.length >= 64 ||
      commandsBlock(pendingRef.current, record, snapshot)
    )
      return;
    const generation = lifecycle.current;
    const id = crypto.randomUUID();
    if (id === record?.stopCommandId || pendingRef.current.some((current) => current.id === id)) {
      showCommandFailure({
        id,
        kind,
        testId,
        text: 'Идентификатор уже занят другой командой. Повторите действие.',
      });
      return;
    }
    const attempt = prepare(id);
    if (new TextEncoder().encode(attempt.originalJSON).length > 8192) {
      showCommandFailure({
        id,
        kind,
        testId,
        text: 'Параметры команды слишком велики. Команда не отправлена.',
      });
      return;
    }
    const item: Pending = {
      id,
      kind,
      testId,
      status: null,
      retired: false,
      receiptRequired: true,
      legacyUnverifiable: false,
      legacyReviewed: false,
      originalJSON: attempt.originalJSON,
      actionId,
    };
    if (!savePending([...pendingRef.current, item])) {
      showCommandFailure({
        id: item.id,
        kind,
        testId,
        text: 'Не удалось сохранить идентификатор. Новая команда не отправлена.',
      });
      return;
    }
    setCommandFailure((previous) =>
      previous?.kind === kind && previous.testId === testId ? null : previous,
    );
    setCommandMessage(item.id, 'Команда отправлена. Ожидаем подтверждения ядра.');
    try {
      const dispatch = await attempt.send();
      if (
        lifecycle.current !== generation ||
        !pendingRef.current.some((current) => samePending(current, item))
      )
        return;
      if (!validDispatchEnvelope(dispatch)) {
        const current = pendingRef.current.find((pendingItem) => samePending(pendingItem, item));
        if (current && (!current.status || current.status.status === 'unknown'))
          unknownMessage(item.id);
        return;
      }
      if (dispatch.delivery === 'notSent' && dispatch.command === null) {
        const existing = pendingRef.current.find((current) => samePending(current, item));
        if (existing?.status && existing.status.status !== 'unknown') {
          if (terminalStatus(existing.status)) return;
          setCommandMessage(
            item.id,
            'Ранее найденное состояние команды требует проверки; новый запрос не отправлен.',
          );
          return;
        }
        showCommandFailure({
          id: item.id,
          kind,
          testId,
          text: 'Команда не отправлена: связь или состояние изменились. Повторите действие после восстановления.',
        });
        forget(item);
        return;
      }
      const result = dispatch.command;
      if (
        dispatch.delivery !== 'response' ||
        !confirmedCommandStatus(result, item.id, item.testId, false)
      ) {
        const current = pendingRef.current.find((pendingItem) => samePending(pendingItem, item));
        if (current && (!current.status || current.status.status === 'unknown'))
          unknownMessage(item.id);
        return;
      }
      const existing = pendingRef.current.find((current) => samePending(current, item));
      if (!existing) return;
      const merged = mergeStatus(existing.status, result);
      memory.statuses.set(pendingKey(item), merged);
      if (existing.receiptRequired && result.status !== 'unknown') {
        savePending(
          pendingRef.current.map((current) =>
            samePending(current, item)
              ? { ...current, receiptRequired: false, status: merged }
              : current,
          ),
          true,
        );
      }
      pendingRef.current = pendingRef.current.map((current) =>
        samePending(current, item) ? { ...current, status: merged } : current,
      );
      if (memory.volatile) memory.volatile = pendingRef.current;
      setPending(pendingRef.current);
      if (merged.status === 'rejected' || merged.status === 'failed') {
        showCommandFailure({
          id: item.id,
          kind,
          testId,
          text: 'Команда отклонена или не выполнена.',
        });
        forget(item);
      } else
        setCommandMessage(
          item.id,
          merged.status === 'completed'
            ? 'Действие подтверждено ядром; ожидаем состояние.'
            : 'Команда принята или её исход ещё не подтверждён.',
        );
    } catch (error) {
      const current = pendingRef.current.find((pendingItem) => samePending(pendingItem, item));
      if (lifecycle.current === generation && current && error instanceof CommandConflictError) {
        showCommandFailure({
          id: item.id,
          kind,
          testId,
          text: 'Идентификатор принадлежит другой команде. Эта попытка отклонена.',
        });
        forget(item);
        return;
      }
      if (
        lifecycle.current === generation &&
        current &&
        (!current.status || current.status.status === 'unknown')
      )
        unknownMessage(item.id);
    }
  }
  async function stop() {
    if (!record) return;
    if (!beginCall('stop', record.testId, 2)) {
      setStopMessage('Ожидаем подтверждения предыдущих запросов остановки.');
      return;
    }
    setStopRequestedFor(record.testId);
    const command = { commandId: record.stopCommandId };
    const originalJSON = JSON.stringify(command);
    const stopKey = stopNoticeKey(record.stopCommandId, record.testId, originalJSON);
    const attemptNoticeKey = stopAttemptNoticeKey(
      record.stopCommandId,
      record.testId,
      originalJSON,
    );
    setCommandMessage(record.stopCommandId, '', attemptNoticeKey);
    lastStopId.current = stopKey;
    const stopIdentity = JSON.stringify([
      record.stopCommandId,
      'stop',
      record.testId,
      null,
      originalJSON,
    ]);
    const storageUnknownBeforeSend = memory.storageUnknown;
    const existing = pendingRef.current.find(
      (item) =>
        item.id === record.stopCommandId &&
        item.kind === 'stop' &&
        item.testId === record.testId &&
        item.originalJSON === originalJSON,
    );
    memory.removals = memory.removals.filter((item) => pendingKey(item) !== stopIdentity);
    const knownStatus = existing?.status ?? memory.statuses.get(stopIdentity) ?? null;
    if (knownStatus) {
      const previous = stopStatuses.current.get(stopIdentity) ?? null;
      stopStatuses.current.set(stopIdentity, mergeStatus(previous, knownStatus));
    }
    let observationVersion: string | undefined;
    try {
      observationVersion = crypto.randomUUID();
    } catch {
      memory.storageUnknown = true;
      setStorageUnconfirmed(true);
    }
    if (existing) {
      const renewed = { ...existing, retired: false, observationVersion };
      memory.retained = memory.retained.filter((item) => !samePending(item, existing));
      memory.retained.push(renewed);
      savePending(
        pendingRef.current.map((item) => (samePending(item, existing) ? renewed : item)),
        true,
      );
    } else if (!existing) {
      const values: Pending[] = [
        ...pendingRef.current,
        {
          id: record.stopCommandId,
          kind: 'stop',
          testId: record.testId,
          status: knownStatus,
          retired: false,
          receiptRequired: true,
          legacyUnverifiable: false,
          legacyReviewed: false,
          originalJSON,
          actionId: null,
          observationVersion,
        },
      ];
      const attempt = values[values.length - 1];
      if (attempt && values.length <= 66) memory.retained.push(attempt);
      if (values.length <= 65) savePending(values, true);
      else if (values.length === 66) {
        memory.volatile = values;
        pendingRef.current = values;
        setPending(values);
        setVolatileStopTracking(true);
      } else {
        setStorageMessage(
          'Не удалось сохранить попытку остановки: список наблюдений заполнен. Проверьте историю испытания.',
        );
      }
    }
    const generation = lifecycle.current;
    const knownBeforeSend = stopStatuses.current.get(stopIdentity);
    setStopMessage(
      knownBeforeSend && terminalStatus(knownBeforeSend)
        ? stopOutcomeMessage(knownBeforeSend, currentRecord.current)
        : 'Запрошена остановка. Ожидаем подтверждения ядра.',
    );
    try {
      const dispatch = await service.stopTest(record.testId, command);
      if (lifecycle.current !== generation) return;
      if (
        validDispatchEnvelope(dispatch) &&
        dispatch.delivery === 'notSent' &&
        dispatch.command === null
      ) {
        const current = pendingRef.current.find(
          (item) => item.kind === 'stop' && pendingKey(item) === stopIdentity,
        );
        if (
          !existing &&
          !knownStatus &&
          !storageUnknownBeforeSend &&
          !memory.storageUnknown &&
          observationVersion &&
          current?.observationVersion === observationVersion &&
          !current.status &&
          countCalls('stop', record.testId) === 1
        )
          forget(current);
        if (
          currentTest.current === record.testId &&
          !terminalStatus(stopStatuses.current.get(stopIdentity) ?? null)
        )
          setStopMessage(
            'Новый запрос остановки не отправлен. Прежний исход необходимо проверить.',
          );
        return;
      }
      const result =
        validDispatchEnvelope(dispatch) && dispatch.delivery === 'response'
          ? dispatch.command
          : null;
      if (!confirmedCommandStatus(result, record.stopCommandId, record.testId)) {
        if (
          currentTest.current === record.testId &&
          !terminalStatus(stopStatuses.current.get(stopIdentity) ?? null)
        )
          setStopMessage('Исход остановки неизвестен; безопасное давление не подтверждено.');
        return;
      }
      const merged = mergeStatus(stopStatuses.current.get(stopIdentity) ?? null, result);
      stopStatuses.current.set(stopIdentity, merged);
      if (terminalStatus(merged)) setCommandMessage(record.stopCommandId, '', stopKey);
      updateTrackedStatus(record.stopCommandId, record.testId, originalJSON, merged);
      if (currentTest.current === record.testId)
        setStopMessage(stopOutcomeMessage(merged, currentRecord.current));
    } catch (error) {
      const known = stopStatuses.current.get(stopIdentity);
      if (lifecycle.current === generation && error instanceof CommandConflictError) {
        const attempt = pendingRef.current.find(
          (item) =>
            item.kind === 'stop' &&
            item.id === record.stopCommandId &&
            item.testId === record.testId &&
            item.originalJSON === originalJSON,
        );
        if (attempt) {
          savePending(
            pendingRef.current.map((item) =>
              samePending(item, attempt) ? { ...item, retired: true } : item,
            ),
            true,
          );
          setCommandMessage(
            record.stopCommandId,
            'Запрос остановки отклонён: идентификатор принадлежит другой команде.',
            attemptNoticeKey,
          );
        } else if (currentTest.current === record.testId) {
          if (!terminalStatus(known ?? null))
            setStopMessage(
              'Запрос остановки отклонён: идентификатор принадлежит другой команде. Безопасное давление не подтверждено.',
            );
          return;
        }
      }
      if (
        lifecycle.current === generation &&
        currentTest.current === record.testId &&
        known?.status !== 'completed' &&
        known?.status !== 'failed' &&
        known?.status !== 'rejected'
      )
        setStopMessage(
          currentRecord.current?.stage === 'finished'
            ? stopPendingMessage(currentRecord.current)
            : 'Исход остановки неизвестен; безопасное давление не подтверждено.',
        );
    } finally {
      endCall('stop', record.testId);
    }
  }
  const formParameters = record
    ? {
        ...empty,
        id: record.testId,
        valve: record.parameters.valve,
        area: record.parameters.areaM2,
        pmax: record.parameters.pmaxPa,
        measurementSeconds: record.parameters.measurementSeconds,
      }
    : empty;
  return (
    <div className="app-shell">
      <div className="workspace">
        <nav className="sidebar" aria-label="Навигация">
          <div className="brand">
            <strong>ГК80</strong>
            <span>Испытания клапанов</span>
          </div>
          <button type="button" className="nav-item" onClick={() => setShowHistory(true)}>
            Испытания
          </button>
          {record && record.stage !== 'finished' && (
            <button type="button" className="nav-item" onClick={() => setShowHistory(false)}>
              Текущее испытание
            </button>
          )}
        </nav>
        <main className="main-content">
          <p>Демонстрация · тестовые данные</p>
          <SessionStatus
            bridge={session}
            hostRunId={hostRunId}
            backendRunId={backendRunId}
            snapshotAccepted={fresh}
          />
          <p role="status" aria-live="polite" className="live-diagnostics-warning">
            {diagnosticsDegraded ? 'Журнал диагностики недоступен или неполон.' : ''}
          </p>
          <section className="demo-messages" aria-label="Сообщения команд">
            <div className="demo-message-primary">
              <div
                className="demo-stop-message"
                ref={stopMessageRegion}
                tabIndex={stopMessage ? 0 : -1}
              >
                {stopMessage && (
                  <p role="status">
                    {stopRequestedFor && stopRequestedFor !== activeId
                      ? `Испытание ${stopRequestedFor}: `
                      : ''}
                    {stopMessage}
                  </p>
                )}
              </div>
              <div
                className="demo-command-message"
                ref={commandMessageRegion}
                tabIndex={
                  message || commandFailure || storageMessage || commandMessages.size ? 0 : -1
                }
              >
                {storageMessage && <p role="alert">{storageMessage}</p>}
                {message && latestNotice === 'message' && <p role="status">{message}</p>}
                {commandFailure && (
                  <p role="status">
                    {commandFailure.text} (команда {commandFailure.id}
                    {commandFailure.testId && `; испытание ${commandFailure.testId}`})
                  </p>
                )}
                {message && latestNotice !== 'message' && <p role="status">{message}</p>}
                {[...commandMessages].map(([key, { id, text }]) => (
                  <p role="status" key={key}>
                    {commandMessages.size > 1
                      ? `${key === id ? 'Команда' : 'Остановка'} ${id}: `
                      : ''}
                    {text}
                  </p>
                ))}
              </div>
            </div>
            <div
              className="demo-message-list"
              tabIndex={
                legacy.unreadable ||
                legacy.items.length ||
                pending.length ||
                storageUnconfirmed ||
                totalStops >= 63 ||
                stopCount >= 2 ||
                volatileStopTracking
                  ? 0
                  : -1
              }
            >
              {(legacy.unreadable || legacy.items.length > 0) && (
                <p role="alert">
                  {legacy.unreadable
                    ? 'Не удалось прочитать прежний список команд; их возможный исход не подтверждён.'
                    : 'Есть прежние команды с неизвестной принадлежностью к базе; их исход не подтверждён.'}
                  {legacyBlocks &&
                    ' Новые команды недоступны до выяснения их исхода; остановка доступна.'}
                </p>
              )}
              {legacy.unreadable && (
                <button
                  type="button"
                  onClick={() => {
                    setLegacy(readLegacyPending());
                    setLegacyReviewed(false);
                  }}
                >
                  Повторить чтение прежних команд
                </button>
              )}
              {legacy.items.map((item) => (
                <p key={item.id}>
                  Прежняя команда: {legacyCommandLabels[item.kind]}, ID {item.id}
                  {item.testId && `; испытание ${item.testId}`}.
                  {item.kind === 'stop' && ' Безопасное давление не подтверждено.'}
                </p>
              ))}
              {!legacy.unreadable && legacyBlocks && (
                <p>
                  Подтверждение не устанавливает исход команд и не связывает их с текущей базой.{' '}
                  <button type="button" onClick={() => setLegacyReviewed(true)}>
                    Я проверил прежние команды
                  </button>
                </p>
              )}
              {totalStops >= 63 && (
                <p role="alert">
                  Достигнут предел неподтверждённых запросов остановки. Закройте приложение и
                  запустите его снова перед новым испытанием.
                </p>
              )}
              {storageUnconfirmed && (
                <p role="alert">
                  Не удалось восстановить список команд. Новые команды недоступны.{' '}
                  <button
                    type="button"
                    onClick={() => {
                      savePending(pendingRef.current, true);
                    }}
                  >
                    Повторить чтение команд
                  </button>
                </p>
              )}
              {volatileStopTracking && (
                <p role="alert">
                  Исход остановки отслеживается только в памяти либо его сохранение не подтверждено:
                  после перезагрузки или закрытия окна потребуется проверить историю испытания.
                </p>
              )}
              {pending.length >= 64 && (
                <p role="alert">
                  Достигнут предел отслеживаемых команд. Проверьте завершённые испытания и скройте
                  ненужные записи наблюдения.
                </p>
              )}
              {pending.map((item) => (
                <p key={pendingKey(item)}>
                  Команда: {commandStatusLabel(item.status?.status)}. ID {item.id}.{' '}
                  {item.kind === 'stop' && `Остановка испытания ${item.testId ?? 'неизвестно'}. `}
                  {item.kind === 'stop' && 'Безопасное давление не подтверждено. '}
                  {item.legacyUnverifiable &&
                    'Исходное содержимое команды не сохранилось; её исход нельзя установить по идентификатору. Подтверждение проверки не устанавливает исход. '}
                  <button
                    type="button"
                    onClick={() => {
                      void lookup(item, true);
                    }}
                  >
                    Проверить состояние
                  </button>
                  {item.legacyUnverifiable && !item.retired && (
                    <button
                      type="button"
                      onClick={() => {
                        savePending(
                          pendingRef.current.map((current) =>
                            samePending(current, item)
                              ? { ...current, retired: true, legacyReviewed: true }
                              : current,
                          ),
                        );
                      }}
                    >
                      Я проверил эту команду
                    </button>
                  )}
                  {item.retired && (
                    <button
                      type="button"
                      disabled={
                        item.kind === 'stop' &&
                        (countCalls('stop', item.testId ?? '') > 0 ||
                          countCalls(
                            'stopLookup',
                            commandLookupKey(hostRunId, backendRunId, databaseId, item.id),
                          ) > 0)
                      }
                      onClick={() => forget(item)}
                    >
                      {item.legacyUnverifiable
                        ? 'Удалить запись наблюдения'
                        : !terminalStatus(item.status)
                          ? 'Прекратить наблюдение'
                          : 'Скрыть наблюдение команды'}
                    </button>
                  )}
                  {item.retired &&
                    item.kind === 'stop' &&
                    !terminalStatus(item.status) &&
                    ' После удаления автоматическая сверка этой попытки невозможна.'}
                </p>
              ))}
              {stopCount >= 2 && (
                <p role="alert">
                  Ответы на запросы остановки не подтверждены. Закройте приложение и запустите его
                  снова; безопасное давление не подтверждено.
                </p>
              )}
            </div>
          </section>
          {(showHistory || (!record && !showForm)) && (
            <History
              service={service}
              available={fresh}
              disabled={!fresh || blocking || (!!record && record.stage !== 'finished')}
              onNew={() => {
                setShowHistory(false);
                if (!draftAvailable) {
                  setDraft(newDraft());
                  setDraftAvailable(true);
                }
                setShowForm(true);
              }}
              onCopy={(value) => {
                setDraft(
                  draftFrom({
                    ...empty,
                    valve: value.valve,
                    area: value.areaM2,
                    pmax: value.pmaxPa,
                    measurementSeconds: value.measurementSeconds,
                  }),
                );
                setDraftAvailable(true);
                setShowHistory(false);
                setShowForm(true);
              }}
              draftAvailable={draftAvailable}
              onDiscard={() => {
                setDraft(newDraft());
                setDraftAvailable(false);
              }}
            />
          )}
          {!showHistory && (!record || record.stage === 'finished') && showForm && (
            <ParametersScreen
              initial={empty}
              draft={draft}
              setDraft={setDraft}
              live
              pending={pending.some((item) => item.kind === 'create' && !item.retired)}
              unavailable={!fresh || blocking}
              onBack={() => {
                setShowForm(false);
                setShowHistory(true);
              }}
              onContinue={(parameters) => {
                if (!snapshot) return;
                void submit('create', null, null, (commandId) => {
                  const command = {
                    commandId,
                    expectedRevision: snapshot.revision,
                    parameters: {
                      valve: parameters.valve,
                      areaM2: parameters.area,
                      pmaxPa: parameters.pmax,
                      measurementSeconds: parameters.measurementSeconds,
                      direction: TestParametersDirection.TestParametersDirectionPressure,
                      actuator: false,
                    },
                  };
                  return {
                    originalJSON: JSON.stringify(command),
                    send: () => service.createTest(command),
                  };
                });
              }}
            />
          )}
          {!showHistory && showSavedParameters && record?.stage === 'preparing' && (
            <ActiveParametersScreen
              live
              parameters={formParameters}
              onReturn={() => setShowSavedParameters(false)}
            />
          )}
          {!showHistory &&
            !showSavedParameters &&
            record?.stage === 'preparing' &&
            readiness?.testId === record.testId && (
              <ReadinessScreen
                key={`${record.testId}:${readiness.revision}`}
                parameters={formParameters}
                autoCheckFailed={
                  !fresh ||
                  !readinessConfirmed ||
                  !readiness.ready ||
                  readiness.revision !== snapshot?.revision
                }
                readiness={readiness}
                pending={blocking}
                stale={!fresh || !readinessConfirmed || readiness.revision !== snapshot?.revision}
                onBack={() => setShowSavedParameters(true)}
                onStart={(checks) => {
                  if (snapshot)
                    void submit('start', record.testId, null, (commandId) => {
                      const command = {
                        commandId,
                        expectedRevision: snapshot.revision,
                        ...checks,
                      };
                      return {
                        originalJSON: JSON.stringify(command),
                        send: () => service.startTest(record.testId, command),
                      };
                    });
                }}
              />
            )}
          {!showHistory &&
            !showSavedParameters &&
            record?.stage === 'preparing' &&
            readiness?.testId !== record.testId && <p>Готовность ещё не подтверждена.</p>}
          {record && record.stage !== 'finished' && (
            <button
              className="work-button work-button-secondary"
              type="button"
              disabled={stopCount >= 2 || totalStops >= 64}
              onClick={() => {
                void stop();
              }}
            >
              {stopRequested ? 'Повторить запрос остановки' : 'Остановить испытание'}
            </button>
          )}
          {!showHistory && record?.stage === 'running' && snapshot && (
            <LiveRun
              key={record.testId}
              record={record}
              snapshot={snapshot}
              fresh={fresh}
              gapVersion={gapVersion}
              pending={blocking}
              onConfirm={() => {
                const action = snapshot.pendingAction;
                if (action)
                  void submit('confirm', record.testId, action.actionId, (commandId) => {
                    const command = {
                      commandId,
                      expectedRevision: snapshot.revision,
                    };
                    return {
                      originalJSON: JSON.stringify(command),
                      send: () => service.confirmAction(record.testId, action.actionId, command),
                    };
                  });
              }}
            />
          )}
          {!showHistory && record?.stage === 'stopping' && (
            <p>Остановка выполняется; безопасное давление не подтверждено.</p>
          )}
          {!showHistory && !showForm && record?.stage === 'finished' && (
            <ResultScreen
              parameters={formParameters}
              record={record}
              saveError={record.saveStatus === 'failed'}
              interrupted={record.outcome === 'interrupted'}
              active={blocking}
              draftAvailable={draftAvailable}
              onList={() => setShowHistory(true)}
              onNew={() => {
                if (!draftAvailable) {
                  setDraft(draftFrom(formParameters));
                  setDraftAvailable(true);
                }
                setShowForm(true);
              }}
            />
          )}
        </main>
      </div>
    </div>
  );
}
