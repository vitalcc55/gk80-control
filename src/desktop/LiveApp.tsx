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
  type SessionView,
  type Snapshot,
  TestParametersDirection,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { TestParameters as FormParameters } from '../model';
import { readLastSession, type SessionBridge } from './bridge';
import { beginCall, countCalls, endCall, totalCalls, useCallRevision } from './call-limits';
import { CommandConflictError, type DemoService } from './demo-service';
import { History } from './History';
import { LiveRun } from './LiveRun';
import { SessionStatus } from './SessionStatus';

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
type Pending = {
  id: string;
  kind: 'create' | 'start' | 'confirm' | 'stop';
  testId: string | null;
  status: CommandStatus | null;
  retired: boolean;
  receiptRequired: boolean;
  legacyUnverifiable: boolean;
  legacyReviewed: boolean;
  originalJSON: string | null;
  actionId: string | null;
};
function samePending(left: Pending, right: Pending): boolean {
  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.testId === right.testId &&
    left.actionId === right.actionId &&
    left.originalJSON === right.originalJSON
  );
}
function pendingKey(item: Pending): string {
  return JSON.stringify([item.id, item.kind, item.testId, item.actionId, item.originalJSON]);
}
function stopNoticeKey(id: string, testId: string | null): string {
  return JSON.stringify([id, 'stop', testId]);
}
function stopLookupNoticeKey(id: string, testId: string | null): string {
  return JSON.stringify([id, 'stop-lookup', testId]);
}
function stopAttemptNoticeKey(id: string, testId: string | null): string {
  return JSON.stringify([id, 'stop-attempt', testId]);
}
function hasUnstoredStop(values: Pending[], storageKey: string, storage?: Storage): boolean {
  const stored = readStoredPending(storageKey, storage);
  return values.some(
    (item) => item.kind === 'stop' && !stored?.some((current) => samePending(current, item)),
  );
}
type CommandFailure = {
  id: string;
  kind: Exclude<Pending['kind'], 'stop'>;
  testId: string | null;
  text: string;
};
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

const legacyStorageKey = 'gk80.demo.pending-command';
type PendingMemory = { volatile: Pending[] | null; storageUnknown: boolean };
const pendingMemory = new Map<string, PendingMemory>();
function memoryFor(databaseId: string): PendingMemory {
  let memory = pendingMemory.get(databaseId);
  if (!memory) {
    memory = { volatile: null, storageUnknown: false };
    pendingMemory.set(databaseId, memory);
  }
  return memory;
}
function commandLookupKey(host: string, backend: string, database: string, id: string): string {
  return JSON.stringify([host, backend, database, id]);
}
type ReadinessRead = {
  listeners: Set<(value: Readiness | null) => void>;
  inFlight: number;
  attempt: number;
  completed: number;
  success: boolean;
  timer: ReturnType<typeof setTimeout> | undefined;
  start: () => void;
};
const readinessReadsByService = new Map<DemoService, Map<string, ReadinessRead>>();
let readinessReadCount = 0;

function observeReadiness(
  service: DemoService,
  hostRunId: string,
  backendRunId: string,
  testId: string,
  revision: number,
  listener: (value: Readiness | null) => void,
) {
  let readinessReads = readinessReadsByService.get(service);
  if (!readinessReads) {
    readinessReads = new Map<string, ReadinessRead>();
    readinessReadsByService.set(service, readinessReads);
  }
  const reads = readinessReads;
  const key = `${hostRunId}:${backendRunId}:${testId}:${revision}`;
  let pending = reads.get(key);
  if (!pending) {
    if (readinessReadCount >= 64) {
      if (!reads.size) readinessReadsByService.delete(service);
      queueMicrotask(() => listener(null));
      return () => {};
    }
    pending = {
      listeners: new Set(),
      inFlight: 0,
      attempt: 0,
      completed: 0,
      success: false,
      timer: undefined,
      start: () => {},
    };
    reads.set(key, pending);
    readinessReadCount += 1;
    const current = pending;
    const forgetRead = () => {
      if (reads.get(key) !== current) return;
      reads.delete(key);
      readinessReadCount -= 1;
      if (!reads.size) readinessReadsByService.delete(service);
    };
    const schedule = (delay: number) => {
      if (!current.listeners.size || current.success || current.timer) return;
      current.timer = setTimeout(() => {
        current.timer = undefined;
        current.start();
      }, delay);
    };
    current.start = () => {
      if (!current.listeners.size || current.success || current.inFlight >= 2) return;
      current.inFlight += 1;
      const attempt = ++current.attempt;
      const watchdog = setTimeout(() => schedule(0), 4000);
      void service
        .readiness(testId)
        .then(
          (value) => {
            if (attempt <= current.completed) return;
            current.completed = attempt;
            const result = value.testId === testId && value.revision === revision ? value : null;
            current.success = result !== null && current.listeners.size > 0;
            for (const callback of current.listeners) callback(result);
          },
          () => {
            if (attempt <= current.completed) return;
            current.completed = attempt;
            for (const callback of current.listeners) callback(null);
          },
        )
        .finally(() => {
          clearTimeout(watchdog);
          current.inFlight -= 1;
          if (current.listeners.size) schedule(2000);
          else if (!current.inFlight) forgetRead();
        });
    };
  }
  const current = pending;
  current.listeners.add(listener);
  if (!current.inFlight) current.start();
  else if (!current.timer && !current.success)
    current.timer = setTimeout(() => {
      current.timer = undefined;
      current.start();
    }, 4000);
  return () => {
    current.listeners.delete(listener);
    if (!current.listeners.size) {
      current.success = false;
      clearTimeout(current.timer);
      current.timer = undefined;
      if (!current.inFlight && reads.get(key) === current) {
        reads.delete(key);
        readinessReadCount -= 1;
        if (!reads.size) readinessReadsByService.delete(service);
      }
    }
  };
}

function mergeStatus(previous: CommandStatus | null, next: CommandStatus): CommandStatus {
  if (
    previous &&
    (previous.status === 'completed' ||
      previous.status === 'rejected' ||
      previous.status === 'failed')
  )
    return !previous.testId && next.testId && previous.status === next.status
      ? { ...previous, testId: next.testId }
      : previous;
  if (previous?.status === 'accepted' && next.status === 'unknown') return previous;
  return next;
}

function terminalStatus(status: CommandStatus | null): boolean {
  return (
    status?.status === 'completed' || status?.status === 'rejected' || status?.status === 'failed'
  );
}

function validUUID(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value) &&
    value !== '00000000-0000-0000-0000-000000000000'
  );
}

function confirmedCommandStatus(
  value: unknown,
  id: string,
  testId: string | null,
  requireTest = true,
): value is CommandStatus {
  if (
    typeof value !== 'object' ||
    value === null ||
    !('commandId' in value) ||
    !('status' in value) ||
    value.commandId !== id ||
    typeof value.status !== 'string' ||
    !['accepted', 'completed', 'rejected', 'failed'].includes(value.status) ||
    Object.keys(value).some((key) => !['commandId', 'status', 'testId', 'reason'].includes(key))
  )
    return false;
  const resultTest = 'testId' in value ? value.testId : null;
  const reason = 'reason' in value ? value.reason : null;
  if (reason !== null && typeof reason !== 'string') return false;
  if (resultTest !== null && !validUUID(resultTest)) return false;
  if (requireTest && value.status !== 'rejected' && resultTest === null) return false;
  return (
    testId === null ||
    resultTest === testId ||
    (resultTest === null && (!requireTest || value.status === 'rejected'))
  );
}

function validExactEnvelope(value: unknown): value is { outcome: string; command: unknown } {
  return (
    typeof value === 'object' &&
    value !== null &&
    'outcome' in value &&
    typeof value.outcome === 'string' &&
    'command' in value &&
    Object.keys(value).every((key) => key === 'outcome' || key === 'command')
  );
}

function validDispatchEnvelope(value: unknown): value is { delivery: string; command: unknown } {
  return (
    typeof value === 'object' &&
    value !== null &&
    'delivery' in value &&
    typeof value.delivery === 'string' &&
    'command' in value &&
    Object.keys(value).every((key) => key === 'delivery' || key === 'command')
  );
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

function commandsBlock(
  items: Pending[],
  record: Snapshot['activeTest'],
  snapshot: Snapshot | null,
): boolean {
  return items.some(
    (item) =>
      !item.retired &&
      item.kind !== 'stop' &&
      !(
        item.kind === 'start' &&
        item.testId === record?.testId &&
        item.status?.status === 'accepted' &&
        record.stage === 'running' &&
        !!snapshot?.pendingAction
      ),
  );
}

function parsePending(raw: unknown, scoped = true): Pending | null {
  if (
    typeof raw !== 'object' ||
    raw === null ||
    !('id' in raw) ||
    !('kind' in raw) ||
    !('testId' in raw) ||
    !('retired' in raw)
  )
    return null;
  if (
    typeof raw.id !== 'string' ||
    (raw.kind !== 'create' &&
      raw.kind !== 'start' &&
      raw.kind !== 'confirm' &&
      raw.kind !== 'stop') ||
    (typeof raw.testId !== 'string' && raw.testId !== null) ||
    typeof raw.retired !== 'boolean' ||
    ('receiptRequired' in raw && typeof raw.receiptRequired !== 'boolean') ||
    ('legacyReviewed' in raw && typeof raw.legacyReviewed !== 'boolean')
  )
    return null;
  let originalJSON: string | null = null;
  let actionId: string | null = null;
  if ('schema' in raw && raw.schema === 2) {
    if (
      !('originalJSON' in raw) ||
      typeof raw.originalJSON !== 'string' ||
      new TextEncoder().encode(raw.originalJSON).length > 8192 ||
      !('actionId' in raw) ||
      (raw.actionId !== null && typeof raw.actionId !== 'string')
    )
      return null;
    try {
      const body: unknown = JSON.parse(raw.originalJSON);
      if (
        typeof body !== 'object' ||
        body === null ||
        !('commandId' in body) ||
        body.commandId !== raw.id
      )
        return null;
    } catch {
      return null;
    }
    if (
      (raw.kind === 'create' && (raw.testId !== null || raw.actionId !== null)) ||
      (raw.kind === 'start' && (typeof raw.testId !== 'string' || raw.actionId !== null)) ||
      (raw.kind === 'stop' && (typeof raw.testId !== 'string' || raw.actionId !== null)) ||
      (raw.kind === 'confirm' &&
        (typeof raw.testId !== 'string' || typeof raw.actionId !== 'string'))
    )
      return null;
    originalJSON = raw.originalJSON;
    actionId = raw.actionId;
  }
  const legacyUnverifiable = scoped && originalJSON === null;
  return {
    id: raw.id,
    kind: raw.kind,
    testId: raw.testId,
    retired:
      raw.kind === 'stop' || !scoped || !legacyUnverifiable
        ? raw.retired
        : 'legacyReviewed' in raw && raw.legacyReviewed === true,
    status: null,
    receiptRequired: scoped,
    legacyUnverifiable,
    legacyReviewed: 'legacyReviewed' in raw && raw.legacyReviewed === true,
    originalJSON,
    actionId,
  };
}
function readStoredPending(storageKey: string, storage?: Storage): Pending[] | null {
  try {
    const raw: unknown = JSON.parse((storage ?? sessionStorage).getItem(storageKey) ?? '[]');
    if (!Array.isArray(raw) || raw.length > 65) return null;
    const result: Pending[] = [];
    for (const item of raw) {
      const parsed = parsePending(item);
      if (!parsed) return null;
      result.push(parsed);
    }
    return result;
  } catch {
    return null;
  }
}
function restorePending(
  storageKey: string,
  memory: PendingMemory,
  storage?: Storage,
): Pending[] | null {
  if (memory.volatile) return memory.storageUnknown ? null : restoredMemory(memory.volatile);
  const stored = readStoredPending(storageKey, storage);
  if (stored === null) memory.storageUnknown = true;
  return memory.storageUnknown ? null : stored;
}

function restoredMemory(items: Pending[]): Pending[] {
  return items.map((item) => ({
    ...item,
    status: item.kind === 'stop' && item.originalJSON !== null ? item.status : null,
    receiptRequired: true,
    legacyUnverifiable: item.originalJSON === null,
  }));
}

function readLegacyPending(): { items: Pending[]; unreadable: boolean } {
  try {
    const raw = sessionStorage.getItem(legacyStorageKey);
    if (raw === null) return { items: [], unreadable: false };
    const values: unknown = JSON.parse(raw);
    if (!Array.isArray(values) || values.length > 65) return { items: [], unreadable: true };
    const items: Pending[] = [];
    for (const value of values) {
      const item = parsePending(value, false);
      if (!item) return { items: [], unreadable: true };
      items.push(item);
    }
    return { items, unreadable: false };
  } catch {
    return { items: [], unreadable: true };
  }
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

export function LiveApp(props: LiveAppProps) {
  const [draft, setDraft] = useState(newDraft);
  const [draftAvailable, setDraftAvailable] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  if (props.suspended) return null;
  return (
    <ActiveLiveApp
      key={`${props.hostRunId}:${props.backendRunId}:${props.databaseId}`}
      {...props}
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
  draft,
  setDraft,
  draftAvailable,
  setDraftAvailable,
  showForm,
  setShowForm,
  showHistory,
  setShowHistory,
}: LiveAppProps & {
  draft: ReturnType<typeof newDraft>;
  setDraft: Dispatch<SetStateAction<ReturnType<typeof newDraft>>>;
  draftAvailable: boolean;
  setDraftAvailable: Dispatch<SetStateAction<boolean>>;
  showForm: boolean;
  setShowForm: Dispatch<SetStateAction<boolean>>;
  showHistory: boolean;
  setShowHistory: Dispatch<SetStateAction<boolean>>;
}) {
  const [connection, setConnection] = useState<{ view: SessionView; expiresAt: number } | null>(
    null,
  );
  const [lastSnapshot, setLastSnapshot] = useState<Snapshot | null>(null);
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
  const [gapVersion, setGapVersion] = useState(0);
  const onGap = useCallback(() => setGapVersion((value) => value + 1), []);
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
  const knownView = useRef<{ revision: number; sequence: number; measurement: number } | null>(
    null,
  );
  const lastKnownSnapshot = useRef<Snapshot | null>(null);
  const rememberView = useCallback(
    (view: SessionView) => {
      const next = view.snapshot;
      if (view.connection.hostRunId !== hostRunId || next?.backendRunId !== backendRunId)
        return false;
      const current = knownView.current;
      const measurement = Math.max(0, ...(next.measurements ?? []).map((item) => item.sequence));
      const revision = view.connection.revision;
      if (!Number.isSafeInteger(revision) || !Number.isSafeInteger(next.lastEventSequence))
        return false;
      if (
        current &&
        (revision < current.revision ||
          next.lastEventSequence < current.sequence ||
          (revision === current.revision &&
            next.lastEventSequence === current.sequence &&
            measurement < current.measurement))
      )
        return false;
      knownView.current = { revision, sequence: next.lastEventSequence, measurement };
      lastKnownSnapshot.current = next;
      setLastSnapshot(next);
      return true;
    },
    [backendRunId, hostRunId],
  );
  const onView = useCallback(
    (view: SessionView | null, expiresAt: number) => {
      if (view?.snapshot && rememberView(view)) setConnection({ view, expiresAt });
      else setConnection(null);
    },
    [rememberView],
  );
  useEffect(
    () =>
      readLastSession(session, (view) => {
        const previous = knownView.current;
        const previousSnapshot = lastKnownSnapshot.current;
        if (!rememberView(view) || !previous || !view.snapshot) return;
        if (previousSnapshot && JSON.stringify(previousSnapshot) !== JSON.stringify(view.snapshot))
          setConnection(null);
      }),
    [session, rememberView],
  );
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
    performance.now() < connection.expiresAt &&
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
    (values: Pending[], allowVolatile = false): boolean => {
      try {
        if (memory.storageUnknown) {
          const stored = readStoredPending(storageKey, pendingStorage);
          if (!stored) throw new Error('pending storage not restored');
          const merged = new Map(stored.map((item) => [pendingKey(item), item]));
          for (const item of values) {
            merged.set(pendingKey(item), item);
          }
          values = [...merged.values()];
          if (values.length > 65) throw new Error('pending list full');
        }
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
              }) => ({
                id,
                kind,
                testId,
                retired,
                receiptRequired,
                legacyReviewed,
                ...(originalJSON === null ? {} : { schema: 2, originalJSON, actionId }),
              }),
            ),
          ),
        );
        pendingRef.current = values;
        setPending(values);
        memory.volatile = null;
        memory.storageUnknown = false;
        setStorageUnconfirmed(false);
        setVolatileStopTracking(false);
        setStorageMessage('');
        return true;
      } catch {
        if (allowVolatile) {
          memory.volatile = values;
          pendingRef.current = values;
          setPending(values);
          setVolatileStopTracking(hasUnstoredStop(values, storageKey, pendingStorage));
        }
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
      if (pendingRef.current.some((current) => samePending(current, item)))
        savePending(
          pendingRef.current.filter((current) => !samePending(current, item)),
          true,
        );
      setCommandMessage(
        item.id,
        '',
        item.kind === 'stop' ? stopNoticeKey(item.id, item.testId) : item.id,
      );
      if (item.kind === 'stop')
        setCommandMessage(item.id, '', stopLookupNoticeKey(item.id, item.testId));
      if (item.kind === 'stop')
        setCommandMessage(item.id, '', stopAttemptNoticeKey(item.id, item.testId));
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
            item.kind === 'stop' ? stopNoticeKey(item.id, item.testId) : item.id,
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
          const noticeKey = item.kind === 'stop' ? stopNoticeKey(item.id, item.testId) : item.id;
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
                stopLookupNoticeKey(item.id, item.testId),
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
            setCommandMessage(item.id, '', stopLookupNoticeKey(item.id, item.testId));
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
          const stopKey = stopNoticeKey(item.id, item.testId);
          const stopStatus = mergeStatus(stopStatuses.current.get(stopKey) ?? null, merged);
          stopStatuses.current.set(stopKey, stopStatus);
          merged = stopStatus;
          if (terminalStatus(stopStatus))
            setCommandMessage(item.id, '', stopNoticeKey(item.id, item.testId));
          if (lastStopId.current === stopKey) {
            if (stopStatus.status === 'completed') setStopMessage('Остановка подтверждена ядром.');
            else if (stopStatus.status === 'failed' || stopStatus.status === 'rejected')
              setStopMessage(
                'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.',
              );
          }
        }
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
              lastStopId.current === stopNoticeKey(item.id, item.testId)
            ) {
              lastStopId.current = stopNoticeKey(item.id, item.testId);
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
          item.kind === 'stop'
            ? (stopStatuses.current.get(stopNoticeKey(item.id, item.testId)) ?? null)
            : null;
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
            item.kind === 'stop' ? stopNoticeKey(item.id, item.testId) : item.id,
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
    if (!fresh || (!pending.length && !pendingRef.current.length)) return;
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
      performance.now() >= connection.expiresAt ||
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
    const stopKey = stopNoticeKey(record.stopCommandId, record.testId);
    const attemptNoticeKey = stopAttemptNoticeKey(record.stopCommandId, record.testId);
    setCommandMessage(record.stopCommandId, '', attemptNoticeKey);
    lastStopId.current = stopKey;
    const command = { commandId: record.stopCommandId };
    const originalJSON = JSON.stringify(command);
    const existing = pendingRef.current.find(
      (item) =>
        item.id === record.stopCommandId &&
        item.kind === 'stop' &&
        item.testId === record.testId &&
        item.originalJSON === originalJSON,
    );
    if (existing?.retired) {
      savePending(
        pendingRef.current.map((item) =>
          samePending(item, existing) ? { ...item, retired: false } : item,
        ),
        true,
      );
    } else if (!existing) {
      const values: Pending[] = [
        ...pendingRef.current,
        {
          id: record.stopCommandId,
          kind: 'stop',
          testId: record.testId,
          status: null,
          retired: false,
          receiptRequired: true,
          legacyUnverifiable: false,
          legacyReviewed: false,
          originalJSON,
          actionId: null,
        },
      ];
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
    const knownBeforeSend = stopStatuses.current.get(stopKey);
    setStopMessage(
      knownBeforeSend && terminalStatus(knownBeforeSend)
        ? stopOutcomeMessage(knownBeforeSend, currentRecord.current)
        : 'Запрошена остановка. Ожидаем подтверждения ядра.',
    );
    try {
      const result = await service.stopTest(record.testId, command);
      if (lifecycle.current !== generation) return;
      if (!confirmedCommandStatus(result, record.stopCommandId, record.testId)) {
        if (
          currentTest.current === record.testId &&
          !terminalStatus(stopStatuses.current.get(stopKey) ?? null)
        )
          setStopMessage('Исход остановки неизвестен; безопасное давление не подтверждено.');
        return;
      }
      const merged = mergeStatus(stopStatuses.current.get(stopKey) ?? null, result);
      stopStatuses.current.set(stopKey, merged);
      if (terminalStatus(merged))
        setCommandMessage(
          record.stopCommandId,
          '',
          stopNoticeKey(record.stopCommandId, record.testId),
        );
      updateTrackedStatus(record.stopCommandId, record.testId, originalJSON, merged);
      if (currentTest.current === record.testId)
        setStopMessage(stopOutcomeMessage(merged, currentRecord.current));
    } catch (error) {
      const known = stopStatuses.current.get(stopKey);
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
            onView={onView}
            onGap={onGap}
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
