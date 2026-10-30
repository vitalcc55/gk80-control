import { useCallback, useEffect, useRef, useState } from 'react';
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
import {
  Command,
  ConfirmAction,
  CreateTest,
  Readiness as ReadTestReadiness,
  StartTest,
  StopTest,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/service';
import type { TestParameters as FormParameters } from '../model';
import { readLastSession, type SessionBridge } from './bridge';
import { beginCall, countCalls, endCall, totalCalls, useCallRevision } from './call-limits';
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
};
const storageKey = 'gk80.demo.pending-command';
let volatilePending: Pending[] | null = null;
let pendingStorageUnknown = false;
type ReadinessRead = {
  listeners: Set<(value: Readiness | null) => void>;
  inFlight: number;
  attempt: number;
  completed: number;
  success: boolean;
  timer: ReturnType<typeof setTimeout> | undefined;
  start: () => void;
};
const readinessReads = new Map<string, ReadinessRead>();

function observeReadiness(
  testId: string,
  revision: number,
  listener: (value: Readiness | null) => void,
) {
  const key = `${testId}:${revision}`;
  let pending = readinessReads.get(key);
  if (!pending) {
    if (readinessReads.size >= 64) {
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
    readinessReads.set(key, pending);
    const current = pending;
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
      void ReadTestReadiness(testId)
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
          else if (!current.inFlight) readinessReads.delete(key);
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
      if (!current.inFlight) readinessReads.delete(key);
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
    return previous;
  if (previous?.status === 'accepted' && next.status === 'unknown') return previous;
  return next;
}

function stopPendingMessage(record: Snapshot['activeTest']): string {
  return record?.stage === 'finished' && record.outcome === 'interrupted'
    ? 'Испытание прервано по состоянию ядра; исход запроса остановки ещё не подтверждён.'
    : 'Остановка запрошена, её завершение ещё не подтверждено.';
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

function parsePending(raw: unknown): Pending | null {
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
    typeof raw.retired !== 'boolean'
  )
    return null;
  return { id: raw.id, kind: raw.kind, testId: raw.testId, retired: raw.retired, status: null };
}
function readStoredPending(): Pending[] | null {
  try {
    const raw: unknown = JSON.parse(sessionStorage.getItem(storageKey) ?? '[]');
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
function restorePending(): Pending[] | null {
  if (volatilePending) return pendingStorageUnknown ? null : [...volatilePending];
  const stored = readStoredPending();
  if (stored === null) pendingStorageUnknown = true;
  return pendingStorageUnknown ? null : stored;
}

export function LiveApp({
  session,
  hostRunId,
  backendRunId,
  diagnosticsDegraded = false,
}: {
  session: SessionBridge;
  hostRunId: string;
  backendRunId: string;
  diagnosticsDegraded?: boolean;
}) {
  const [connection, setConnection] = useState<{ view: SessionView; expiresAt: number } | null>(
    null,
  );
  const [draft, setDraft] = useState(newDraft);
  const [lastSnapshot, setLastSnapshot] = useState<Snapshot | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [draftAvailable, setDraftAvailable] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  const [showSavedParameters, setShowSavedParameters] = useState(false);
  const [readiness, setReadiness] = useState<Readiness | null>(null);
  const [readinessConfirmed, setReadinessConfirmed] = useState(false);
  const [restored] = useState(restorePending);
  const [storageUnconfirmed, setStorageUnconfirmed] = useState(
    restored === null || pendingStorageUnknown,
  );
  const [pending, setPending] = useState<Pending[]>(restored ?? volatilePending ?? []);
  const pendingRef = useRef(pending);
  const [message, setMessage] = useState('');
  const [stopMessage, setStopMessageValue] = useState('');
  const stopMessageRegion = useRef<HTMLDivElement>(null);
  const stopMessageValue = useRef('');
  const setStopMessage = useCallback((value: string) => {
    if (value !== stopMessageValue.current && stopMessageRegion.current)
      stopMessageRegion.current.scrollTop = 0;
    stopMessageValue.current = value;
    setStopMessageValue(value);
  }, []);
  const [volatileStopTracking, setVolatileStopTracking] = useState(() => volatilePending !== null);
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
    storageUnconfirmed ||
    pending.length >= 64 ||
    totalStops >= 63 ||
    commandsBlock(pending, record, snapshot);

  useEffect(() => {
    setReadinessConfirmed(false);
    if (fresh && stage === 'preparing' && activeId && revision !== undefined) {
      return observeReadiness(activeId, revision, (value) => {
        if (value) {
          setReadiness(value);
          setReadinessConfirmed(true);
        } else {
          setReadinessConfirmed(false);
          setMessage('Не удалось подтвердить готовность.');
        }
      });
    }
    return undefined;
  }, [fresh, activeId, stage, revision]);

  const savePending = useCallback((values: Pending[], allowVolatile = false): boolean => {
    try {
      if (pendingStorageUnknown) {
        const stored = readStoredPending();
        if (!stored) throw new Error('pending storage not restored');
        const merged = new Map(stored.map((item) => [item.id, item]));
        for (const item of values) {
          const previous = merged.get(item.id);
          if (previous && (previous.kind !== item.kind || previous.testId !== item.testId))
            throw new Error('conflicting pending command');
          merged.set(item.id, item);
        }
        values = [...merged.values()];
        if (values.length > 65) throw new Error('pending list full');
      }
      sessionStorage.setItem(
        storageKey,
        JSON.stringify(
          values.map(({ id, kind, testId, retired }) => ({ id, kind, testId, retired })),
        ),
      );
      pendingRef.current = values;
      setPending(values);
      volatilePending = null;
      pendingStorageUnknown = false;
      setStorageUnconfirmed(false);
      setVolatileStopTracking(false);
      return true;
    } catch {
      if (allowVolatile) {
        volatilePending = values;
        pendingRef.current = values;
        setPending(values);
        setVolatileStopTracking(true);
      }
      setMessage(
        'Не удалось обновить список команд в интерфейсе; проверьте их состояние перед продолжением.',
      );
      return false;
    }
  }, []);
  const forget = useCallback(
    (id: string) => {
      if (pendingRef.current.some((item) => item.id === id))
        savePending(
          pendingRef.current.filter((item) => item.id !== id),
          true,
        );
    },
    [savePending],
  );
  const lookup = useCallback(
    async (item: Pending, manual = false) => {
      if (!beginCall('lookup', item.id, manual ? 2 : 1)) {
        if (manual) setMessage('Ожидаем завершения предыдущих проверок состояния команды.');
        return;
      }
      const generation = lifecycle.current;
      try {
        const status = await Command(item.id);
        if (lifecycle.current !== generation || status.commandId !== item.id) return;
        const existing = pendingRef.current.find((current) => current.id === item.id);
        if (!existing) return;
        const merged = mergeStatus(existing.status, status);
        if (item.kind === 'stop') {
          const stopStatus = mergeStatus(stopStatuses.current.get(item.id) ?? null, merged);
          stopStatuses.current.set(item.id, stopStatus);
          if (lastStopId.current === item.id) {
            if (stopStatus.status === 'completed') setStopMessage('Остановка подтверждена ядром.');
            else if (stopStatus.status === 'failed' || stopStatus.status === 'rejected')
              setStopMessage(
                'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.',
              );
          }
        }
        if (JSON.stringify(existing.status) !== JSON.stringify(merged)) {
          pendingRef.current = pendingRef.current.map((current) =>
            current.id === item.id ? { ...current, status: merged } : current,
          );
          setPending(pendingRef.current);
        }
        if (merged.status === 'rejected' || merged.status === 'failed') {
          setMessage('Команда отклонена или не выполнена.');
          forget(item.id);
        }
      } catch {
        if (
          lifecycle.current === generation &&
          pendingRef.current.some((current) => current.id === item.id)
        )
          setMessage('Исход команды пока неизвестен. Повторная команда не отправлена.');
      } finally {
        endCall('lookup', item.id);
      }
    },
    [forget, setStopMessage],
  );
  useEffect(() => {
    if (!pending.length || !fresh) return;
    for (const item of pending) {
      const expected = item.status?.testId ?? item.testId;
      if (
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
        forget(item.id);
        return;
      }
    }
    if (
      record?.stage === 'finished' &&
      pending.some((item) => !item.retired && item.testId === record.testId)
    ) {
      savePending(
        pending.map((item) => (item.testId === record.testId ? { ...item, retired: true } : item)),
        true,
      );
      return;
    }
    const item = pending[lookupIndex.current++ % pending.length];
    if (item) void lookup(item);
  }, [pending, fresh, record, snapshot, lookup, forget, savePending]);

  useEffect(() => {
    if (!stopRequested || !record || !fresh) return;
    const generation = lifecycle.current;
    const id = record.stopCommandId;
    const known = stopStatuses.current.get(id);
    if (known?.status === 'completed' || known?.status === 'failed' || known?.status === 'rejected')
      return;
    if (record.stage === 'finished' && record.outcome === 'interrupted')
      setStopMessage(stopPendingMessage(record));
    if (!beginCall('lookup', id, 1)) return;
    void Command(id)
      .then(
        (status) => {
          if (
            lifecycle.current !== generation ||
            status.commandId !== id ||
            currentTest.current !== record.testId
          )
            return;
          const merged = mergeStatus(stopStatuses.current.get(id) ?? null, status);
          stopStatuses.current.set(id, merged);
          if (merged.status === 'completed') setStopMessage('Остановка подтверждена ядром.');
          else if (merged.status === 'failed' || merged.status === 'rejected')
            setStopMessage(
              'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.',
            );
        },
        () => {},
      )
      .finally(() => {
        endCall('lookup', id);
      });
  }, [stopRequested, record, fresh, setStopMessage]);

  async function submit(
    kind: Exclude<Pending['kind'], 'stop'>,
    testId: string | null,
    send: (id: string) => Promise<CommandDispatch>,
  ) {
    if (
      !fresh ||
      !snapshot ||
      !connection ||
      performance.now() >= connection.expiresAt ||
      storageUnconfirmed ||
      totalCalls('stop') >= 63 ||
      pendingRef.current.length >= 64 ||
      commandsBlock(pendingRef.current, record, snapshot)
    )
      return;
    const generation = lifecycle.current;
    const item: Pending = { id: crypto.randomUUID(), kind, testId, status: null, retired: false };
    if (!savePending([...pendingRef.current, item])) {
      setMessage('Не удалось сохранить идентификатор. Новая команда не отправлена.');
      return;
    }
    setMessage('Команда отправлена. Ожидаем подтверждения ядра.');
    try {
      const dispatch = await send(item.id);
      if (
        lifecycle.current !== generation ||
        !pendingRef.current.some((current) => current.id === item.id)
      )
        return;
      if (dispatch.delivery === 'notSent' && dispatch.command === null) {
        const existing = pendingRef.current.find((current) => current.id === item.id);
        if (existing?.status && existing.status.status !== 'unknown') {
          setMessage(
            'Ранее найденное состояние команды требует проверки; новый запрос не отправлен.',
          );
          return;
        }
        setMessage(
          'Команда не отправлена: связь или состояние изменились. Повторите действие после восстановления.',
        );
        forget(item.id);
        return;
      }
      const result = dispatch.command;
      if (dispatch.delivery !== 'response' || !result || result.commandId !== item.id) {
        setMessage('Исход команды пока неизвестен. Повторная команда не отправлена.');
        return;
      }
      const existing = pendingRef.current.find((current) => current.id === item.id);
      if (!existing) return;
      const merged = mergeStatus(existing.status, result);
      pendingRef.current = pendingRef.current.map((current) =>
        current.id === item.id ? { ...current, status: merged } : current,
      );
      setPending(pendingRef.current);
      if (merged.status === 'rejected' || merged.status === 'failed') {
        setMessage('Команда отклонена или не выполнена.');
        forget(item.id);
      } else
        setMessage(
          merged.status === 'completed'
            ? 'Действие подтверждено ядром; ожидаем состояние.'
            : 'Команда принята или её исход ещё не подтверждён.',
        );
    } catch {
      if (
        lifecycle.current === generation &&
        pendingRef.current.some((current) => current.id === item.id)
      )
        setMessage('Исход команды пока неизвестен. Повторная команда не отправлена.');
    }
  }
  async function stop() {
    if (!record) return;
    if (!beginCall('stop', record.testId, 2)) {
      setStopMessage('Ожидаем подтверждения предыдущих запросов остановки.');
      return;
    }
    setStopRequestedFor(record.testId);
    lastStopId.current = record.stopCommandId;
    if (
      !pendingRef.current.some((item) => item.id === record.stopCommandId) &&
      pendingRef.current.length < 65
    ) {
      const values: Pending[] = [
        ...pendingRef.current,
        {
          id: record.stopCommandId,
          kind: 'stop',
          testId: record.testId,
          status: null,
          retired: false,
        },
      ];
      savePending(values, true);
    }
    const generation = lifecycle.current;
    setStopMessage('Запрошена остановка. Ожидаем подтверждения ядра.');
    try {
      const result = await StopTest(record.testId, { commandId: record.stopCommandId });
      if (lifecycle.current !== generation || currentTest.current !== record.testId) return;
      if (result.commandId !== record.stopCommandId) {
        setStopMessage('Исход остановки неизвестен; безопасное давление не подтверждено.');
        return;
      }
      const merged = mergeStatus(stopStatuses.current.get(record.stopCommandId) ?? null, result);
      stopStatuses.current.set(record.stopCommandId, merged);
      setStopMessage(
        merged.status === 'completed'
          ? 'Остановка подтверждена ядром.'
          : merged.status === 'rejected' || merged.status === 'failed'
            ? `Остановка не выполнена или отклонена${merged.reason ? `: ${merged.reason}` : '.'} Безопасное давление не подтверждено.`
            : stopPendingMessage(currentRecord.current),
      );
    } catch {
      const known = stopStatuses.current.get(record.stopCommandId);
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
              <div className="demo-command-message" tabIndex={message ? 0 : -1}>
                {message && <p role="status">{message}</p>}
              </div>
            </div>
            <div
              className="demo-message-list"
              tabIndex={
                pending.length ||
                storageUnconfirmed ||
                totalStops >= 63 ||
                stopCount >= 2 ||
                volatileStopTracking
                  ? 0
                  : -1
              }
            >
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
                  Исход остановки отслеживается только в памяти: после перезагрузки или закрытия
                  окна потребуется проверить историю испытания.
                </p>
              )}
              {pending.length >= 64 && (
                <p role="alert">
                  Достигнут предел отслеживаемых команд. Проверьте завершённые испытания и скройте
                  ненужные записи наблюдения.
                </p>
              )}
              {pending.map((item) => (
                <p key={item.id}>
                  Исход команды: {item.status?.status ?? 'неизвестен'}.{' '}
                  {item.kind === 'stop' &&
                    `Остановка испытания ${item.testId ?? 'неизвестно'}; команда ${item.id}. `}
                  <button
                    type="button"
                    onClick={() => {
                      void lookup(item, true);
                    }}
                  >
                    Проверить состояние
                  </button>
                  {item.retired && (
                    <button type="button" onClick={() => forget(item.id)}>
                      Скрыть наблюдение завершённого испытания
                    </button>
                  )}
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
                void submit('create', null, (commandId) =>
                  CreateTest({
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
                  }),
                );
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
                    void submit('start', record.testId, (commandId) =>
                      StartTest(record.testId, {
                        commandId,
                        expectedRevision: snapshot.revision,
                        ...checks,
                      }),
                    );
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
                  void submit('confirm', record.testId, (commandId) =>
                    ConfirmAction(record.testId, action.actionId, {
                      commandId,
                      expectedRevision: snapshot.revision,
                    }),
                  );
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
