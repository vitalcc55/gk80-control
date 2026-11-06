import type {
  CommandDispatch,
  CommandStatus,
  Snapshot,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { TestParametersDirection } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { StartCommandInput } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/models';
import type { TestParameters as FormParameters } from '../model';
import { beginCall, countCalls, endCall, subscribeCalls, totalCalls } from './call-limits';
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
import type { SessionProjection } from './session-observation';

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

function reservedStopKey(
  id: string,
  testId: string,
  originalJSON = JSON.stringify({ commandId: id }),
) {
  return JSON.stringify([id, 'stop', testId, null, originalJSON]);
}

export type CommandInput = {
  service: DemoService;
  connection: SessionProjection['connection'];
  lastSnapshot: Snapshot | null;
  isCurrentConnection: (connection: SessionProjection['connection']) => boolean;
};

// Consume the accepted projection from #15; no clocks, ordering or lease policy here.
export function commandObservation({
  connection,
  lastSnapshot,
}: Pick<CommandInput, 'connection' | 'lastSnapshot'>) {
  const snapshot = connection?.view.snapshot ?? lastSnapshot;
  const record = snapshot?.activeTest ?? null;
  const fresh =
    !!connection &&
    !!snapshot &&
    Number.isSafeInteger(snapshot.revision) &&
    Number.isSafeInteger(snapshot.lastEventSequence);
  return { connection, snapshot, record, fresh };
}
type Identity = {
  hostRunId: string;
  backendRunId: string;
  databaseId: string;
  pendingStorage?: Storage;
};

export function createCommandCoordinator({
  hostRunId,
  backendRunId,
  databaseId,
  pendingStorage,
}: Identity) {
  const storageKey = `gk80.demo.pending-command.v1:${databaseId}`;
  let active = false;
  let generationVersion = 0;
  let memory: ReturnType<typeof memoryFor>;
  let pending: Pending[] = [];
  let legacy: ReturnType<typeof readLegacyPending> = { items: [], unreadable: false };
  let legacyReviewed = false;
  let storageUnconfirmed = true;
  let message = '';
  let commandFailure: CommandFailure | null = null;
  let latestNotice: 'message' | 'commandFailure' = 'message';
  let storageMessage = '';
  let commandMessages = new Map<
    string,
    { id: string; text: string; startAcceptedFor?: string; attention?: true }
  >();
  let stopMessage = '';
  let volatileStopTracking = false;
  let stopRequestedFor: string | null = null;
  let lastStopId: string | null = null;
  // A rejected response may omit testId; the physical Stop slot uses the attempt's testId.
  const stopStatuses = new Map<string, { testId: string | null; status: CommandStatus }>();
  let lookupIndex = 0;
  let createdVersion = 0;
  let input: CommandInput;
  const listeners = new Set<() => void>();
  let scheduled = false;
  let reconcileNeeded = false;
  let stopCalls: (() => void) | undefined;
  function currentView() {
    return commandObservation(input ?? { connection: null, lastSnapshot: null });
  }
  function pruneStopStatuses() {
    if (!stopStatuses.size) return;
    const { record } = currentView();
    const currentKey = record && reservedStopKey(record.stopCommandId, record.testId);
    const tracked = memory.storageUnknown
      ? [
          ...pending,
          ...(memory.volatile ?? []),
          ...(memory.baseline ?? []),
          ...memory.retained,
          ...memory.removals,
        ]
      : pending;
    for (const [key, { testId, status }] of stopStatuses) {
      if (
        !terminalStatus(status) ||
        key === currentKey ||
        (testId !== null && countCalls('stop', testId) > 0) ||
        countCalls(
          'stopLookup',
          commandLookupKey(hostRunId, backendRunId, databaseId, status.commandId),
        ) > 0 ||
        tracked.some(
          (item) =>
            item.id === status.commandId && item.kind === 'stop' && pendingKey(item) === key,
        )
      )
        continue;
      stopStatuses.delete(key);
    }
  }
  function legacyBlocked() {
    return (
      legacy.unreadable ||
      (!legacyReviewed && legacy.items.some((item) => !item.retired && item.kind !== 'stop'))
    );
  }
  function view() {
    const { record, snapshot } = currentView();
    const stopCount = record ? countCalls('stop', record.testId) : 0;
    const totalStops = totalCalls('stop');
    return {
      pending,
      legacy,
      legacyBlocks: legacyBlocked(),
      storageUnconfirmed,
      message,
      commandFailure,
      latestNotice,
      storageMessage,
      commandMessages,
      stopMessage,
      volatileStopTracking,
      stopRequestedFor,
      createdVersion,
      stopCount,
      totalStops,
      blocking:
        !active ||
        legacyBlocked() ||
        storageUnconfirmed ||
        pending.length >= 64 ||
        totalStops >= 63 ||
        commandsBlock(pending, record, snapshot),
      stopRequested: !!record && stopRequestedFor === record.testId,
    };
  }
  let snapshot = view();
  function changed(reconcileChange = false) {
    if (!active) return;
    reconcileNeeded ||= reconcileChange;
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (!active) return;
      if (reconcileNeeded) {
        reconcileNeeded = false;
        reconcile();
      }
      pruneStopStatuses();
      snapshot = view();
      for (const listener of listeners) listener();
    });
  }
  function setPending(value: Pending[]) {
    pending = value;
    changed(true);
  }
  function setStorageUnconfirmed(value: boolean) {
    if (storageUnconfirmed !== value) {
      storageUnconfirmed = value;
      changed(true);
    }
  }
  function setVolatileStopTracking(value: boolean) {
    volatileStopTracking = value;
    changed();
  }
  function setStorageMessage(value: string) {
    storageMessage = value;
    changed();
  }
  function setStopMessage(value: string) {
    stopMessage = value;
    changed();
  }
  function setStopRequestedFor(value: string | null) {
    stopRequestedFor = value;
    changed();
  }
  function setCommandFailure(update: (value: CommandFailure | null) => CommandFailure | null) {
    commandFailure = update(commandFailure);
    changed();
  }
  function showMessage(text: string) {
    if (!active) return;
    message = text;
    latestNotice = 'message';
    changed();
  }
  function showCommandFailure(failure: CommandFailure) {
    commandFailure = failure;
    latestNotice = 'commandFailure';
    changed();
  }
  function setCommandMessage(
    id: string,
    text: string,
    key = id,
    startAcceptedFor?: string,
    attention = false,
  ) {
    const previous = commandMessages.get(key);
    if (startAcceptedFor && previous?.attention) return;
    if (
      (previous?.text ?? '') === text &&
      previous?.startAcceptedFor === startAcceptedFor &&
      !!previous?.attention === attention
    )
      return;
    commandMessages = new Map(commandMessages);
    if (text)
      commandMessages.set(key, {
        id,
        text,
        ...(startAcceptedFor ? { startAcceptedFor } : {}),
        ...(attention ? { attention: true as const } : {}),
      });
    else commandMessages.delete(key);
    changed();
  }
  function unknownMessage(id: string, key = id) {
    setCommandMessage(
      id,
      'Исход команды пока неизвестен. Автоматический повтор не выполняется.',
      key,
    );
  }
  const savePending = (values: Pending[], allowVolatile = false, remove?: Pending): boolean => {
    if (!active) return false;
    let recovered = values;
    const requestedRemovals = [...memory.removals];
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
      values = values.filter((item) => !memory.removals.some((entry) => samePending(entry, item)));
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
      pending = values;
      setPending(values);
      for (const removed of [...requestedRemovals, ...memory.removals]) {
        if (removed.kind === 'stop' && !values.some((item) => samePending(item, removed))) {
          setCommandMessage(
            removed.id,
            '',
            stopAttemptNoticeKey(removed.id, removed.testId, removed.originalJSON),
          );
        }
      }
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
        recovered = pending.map((item) => {
          const retained = memory.retained.find((entry) => samePending(entry, item));
          return retained?.observationVersion
            ? { ...item, observationVersion: retained.observationVersion }
            : item;
        });
      }
      memory.volatile = recovered;
      pending = recovered;
      setPending(recovered);
      setVolatileStopTracking(hasUnstoredStop(recovered, storageKey, pendingStorage));
      setStorageMessage(
        'Не удалось обновить список команд в интерфейсе; проверьте их состояние перед продолжением.',
      );
      return false;
    }
  };
  const forget = (item: Pending) => {
    if (pending.some((current) => samePending(current, item)) && !savePending(pending, false, item))
      return;
    setCommandMessage(
      item.id,
      '',
      item.kind === 'stop' ? stopNoticeKey(item.id, item.testId, item.originalJSON) : item.id,
    );
    if (item.kind === 'stop')
      setCommandMessage(item.id, '', stopLookupNoticeKey(item.id, item.testId, item.originalJSON));
    if (item.kind === 'stop')
      setCommandMessage(item.id, '', stopAttemptNoticeKey(item.id, item.testId, item.originalJSON));
  };
  const updateTrackedStatus = (
    id: string,
    testId: string,
    originalJSON: string,
    status: CommandStatus,
  ) => {
    const current = pending.find(
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
    pending = pending.map((item) =>
      samePending(item, current) ? { ...item, status: merged } : item,
    );
    if (memory.volatile) memory.volatile = pending;
    setPending(pending);
  };
  const lookup = async (item: Pending, manual = false) => {
    if (!active) return;
    const service = input.service;
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
    const generation = generationVersion;
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
          generationVersion !== generation ||
          !pending.some((current) => samePending(current, item))
        )
          return;
        const current = pending.find((pendingItem) => samePending(pendingItem, item));
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
      if (generationVersion !== generation || status.commandId !== item.id) return;
      const existing = pending.find((current) => samePending(current, item));
      if (!existing || existing.originalJSON === null) return;
      let merged = mergeStatus(existing.status, status);
      if (existing.receiptRequired && status.status !== 'unknown') {
        savePending(
          pending.map((current) =>
            samePending(current, item) ? { ...current, receiptRequired: false } : current,
          ),
          true,
        );
      }
      if (item.kind === 'stop') {
        const stopKey = stopNoticeKey(item.id, item.testId, item.originalJSON);
        const identity = pendingKey(item);
        const stopStatus = mergeStatus(stopStatuses.get(identity)?.status ?? null, merged);
        stopStatuses.set(identity, { testId: item.testId, status: stopStatus });
        merged = stopStatus;
        if (terminalStatus(stopStatus))
          setCommandMessage(item.id, '', stopNoticeKey(item.id, item.testId, item.originalJSON));
        if (lastStopId === stopKey) {
          if (stopStatus.status === 'completed') setStopMessage('Остановка подтверждена ядром.');
          else if (stopStatus.status === 'failed' || stopStatus.status === 'rejected')
            setStopMessage(
              'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.',
            );
        }
      }
      // Saving the receipt can also commit a previously requested removal. The lookup still
      // reports its outcome, but the removed Stop no longer has an observation to restore.
      if (item.kind !== 'stop' || pending.some((current) => samePending(current, item)))
        memory.statuses.set(pendingKey(item), merged);
      if (JSON.stringify(existing.status) !== JSON.stringify(merged)) {
        pending = pending.map((current) =>
          samePending(current, item) ? { ...current, status: merged } : current,
        );
        if (memory.volatile) memory.volatile = pending;
        setPending(pending);
      }
      if (item.kind !== 'stop' && merged.status === 'completed')
        setCommandMessage(item.id, 'Действие подтверждено ядром; ожидаем состояние.');
      else if (item.kind !== 'stop' && merged.status === 'accepted')
        setCommandMessage(
          item.id,
          'Команда принята или её исход ещё не подтверждён.',
          item.id,
          item.kind === 'start' ? (item.testId ?? undefined) : undefined,
        );
      if (merged.status === 'rejected' || merged.status === 'failed') {
        if (item.kind === 'stop') {
          const failure =
            'Остановка не выполнена или отклонена. Безопасное давление не подтверждено.';
          if (
            lastStopId === null ||
            lastStopId === stopNoticeKey(item.id, item.testId, item.originalJSON)
          ) {
            lastStopId = stopNoticeKey(item.id, item.testId, item.originalJSON);
            setStopRequestedFor(item.testId);
            setStopMessage(failure);
          }
          savePending(
            pending.map((current) =>
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
      const current = pending.find((pendingItem) => samePending(pendingItem, item));
      const knownStop =
        item.kind === 'stop' ? (stopStatuses.get(pendingKey(item))?.status ?? null) : null;
      if (
        generationVersion === generation &&
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
      if (item.kind === 'stop' && !active) pruneStopStatuses();
    }
  };
  function reconcile() {
    const { fresh, record, snapshot } = currentView();
    if (!fresh || storageUnconfirmed || (!pending.length && !pending.length)) return;
    const tracked = pending;
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
          createdVersion += 1;
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
    const item = unresolved[lookupIndex++ % unresolved.length];
    if (item) void lookup(item);
  }
  async function submit(
    kind: Exclude<Pending['kind'], 'stop'>,
    testId: string | null,
    actionId: string | null,
    prepare: (id: string) => { originalJSON: string; send: () => Promise<CommandDispatch> },
  ) {
    if (!active) return;
    const { fresh, record, snapshot, connection } = currentView();
    const { isCurrentConnection } = input;
    if (
      !fresh ||
      !snapshot ||
      !connection ||
      !isCurrentConnection(connection) ||
      legacyBlocked() ||
      storageUnconfirmed ||
      totalCalls('stop') >= 63 ||
      pending.length >= 64 ||
      commandsBlock(pending, record, snapshot)
    )
      return;
    const generation = generationVersion;
    const id = crypto.randomUUID();
    if (id === record?.stopCommandId || pending.some((current) => current.id === id)) {
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
    if (!savePending([...pending, item])) {
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
    try {
      const dispatch = await attempt.send();
      if (
        generationVersion !== generation ||
        !pending.some((current) => samePending(current, item))
      )
        return;
      if (!validDispatchEnvelope(dispatch)) {
        const current = pending.find((pendingItem) => samePending(pendingItem, item));
        if (current && (!current.status || current.status.status === 'unknown'))
          unknownMessage(item.id);
        return;
      }
      if (dispatch.delivery === 'notSent' && dispatch.command === null) {
        const existing = pending.find((current) => samePending(current, item));
        if (existing?.status && existing.status.status !== 'unknown') {
          if (terminalStatus(existing.status)) return;
          setCommandMessage(
            item.id,
            'Ранее найденное состояние команды требует проверки; новый запрос не отправлен.',
            item.id,
            undefined,
            true,
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
        const current = pending.find((pendingItem) => samePending(pendingItem, item));
        if (current && (!current.status || current.status.status === 'unknown'))
          unknownMessage(item.id);
        return;
      }
      const existing = pending.find((current) => samePending(current, item));
      if (!existing) return;
      const merged = mergeStatus(existing.status, result);
      memory.statuses.set(pendingKey(item), merged);
      if (existing.receiptRequired && result.status !== 'unknown') {
        savePending(
          pending.map((current) =>
            samePending(current, item)
              ? { ...current, receiptRequired: false, status: merged }
              : current,
          ),
          true,
        );
      }
      pending = pending.map((current) =>
        samePending(current, item) ? { ...current, status: merged } : current,
      );
      if (memory.volatile) memory.volatile = pending;
      setPending(pending);
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
          item.id,
          merged.status === 'accepted' && item.kind === 'start'
            ? (item.testId ?? undefined)
            : undefined,
        );
    } catch (error) {
      const current = pending.find((pendingItem) => samePending(pendingItem, item));
      if (generationVersion === generation && current && error instanceof CommandConflictError) {
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
        generationVersion === generation &&
        current &&
        (!current.status || current.status.status === 'unknown')
      )
        unknownMessage(item.id);
    }
  }
  async function stop() {
    if (!active) return;
    const { record } = currentView();
    const service = input.service;
    if (!active) return;
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
    lastStopId = stopKey;
    const stopIdentity = reservedStopKey(record.stopCommandId, record.testId, originalJSON);
    const storageUnknownBeforeSend = memory.storageUnknown;
    const existing = pending.find(
      (item) =>
        item.id === record.stopCommandId &&
        item.kind === 'stop' &&
        item.testId === record.testId &&
        item.originalJSON === originalJSON,
    );
    memory.removals = memory.removals.filter((item) => pendingKey(item) !== stopIdentity);
    const knownStatus = existing?.status ?? memory.statuses.get(stopIdentity) ?? null;
    if (knownStatus) {
      const previous = stopStatuses.get(stopIdentity)?.status ?? null;
      stopStatuses.set(stopIdentity, {
        testId: record.testId,
        status: mergeStatus(previous, knownStatus),
      });
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
        pending.map((item) => (samePending(item, existing) ? renewed : item)),
        true,
      );
    } else if (!existing) {
      const values: Pending[] = [
        ...pending,
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
        pending = values;
        setPending(values);
        setVolatileStopTracking(true);
      } else {
        setStorageMessage(
          'Не удалось сохранить попытку остановки: список наблюдений заполнен. Проверьте историю испытания.',
        );
      }
    }
    const generation = generationVersion;
    const knownBeforeSend = stopStatuses.get(stopIdentity)?.status;
    setStopMessage(
      knownBeforeSend && terminalStatus(knownBeforeSend)
        ? stopOutcomeMessage(knownBeforeSend, currentView().record)
        : 'Запрошена остановка. Ожидаем подтверждения ядра.',
    );
    try {
      const dispatch = await service.stopTest(record.testId, command);
      if (generationVersion !== generation) return;
      if (
        validDispatchEnvelope(dispatch) &&
        dispatch.delivery === 'notSent' &&
        dispatch.command === null
      ) {
        const current = pending.find(
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
          currentView().record?.testId === record.testId &&
          !terminalStatus(stopStatuses.get(stopIdentity)?.status ?? null)
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
          currentView().record?.testId === record.testId &&
          !terminalStatus(stopStatuses.get(stopIdentity)?.status ?? null)
        )
          setStopMessage('Исход остановки неизвестен; безопасное давление не подтверждено.');
        return;
      }
      const merged = mergeStatus(stopStatuses.get(stopIdentity)?.status ?? null, result);
      stopStatuses.set(stopIdentity, { testId: record.testId, status: merged });
      if (terminalStatus(merged)) setCommandMessage(record.stopCommandId, '', stopKey);
      updateTrackedStatus(record.stopCommandId, record.testId, originalJSON, merged);
      if (currentView().record?.testId === record.testId)
        setStopMessage(stopOutcomeMessage(merged, currentView().record));
    } catch (error) {
      const known = stopStatuses.get(stopIdentity)?.status;
      if (generationVersion === generation && error instanceof CommandConflictError) {
        const attempt = pending.find(
          (item) =>
            item.kind === 'stop' &&
            item.id === record.stopCommandId &&
            item.testId === record.testId &&
            item.originalJSON === originalJSON,
        );
        if (attempt) {
          savePending(
            pending.map((item) => (samePending(item, attempt) ? { ...item, retired: true } : item)),
            true,
          );
          setCommandMessage(
            record.stopCommandId,
            'Запрос остановки отклонён: идентификатор принадлежит другой команде.',
            attemptNoticeKey,
          );
        } else if (currentView().record?.testId === record.testId) {
          if (!terminalStatus(known ?? null))
            setStopMessage(
              'Запрос остановки отклонён: идентификатор принадлежит другой команде. Безопасное давление не подтверждено.',
            );
          return;
        }
      }
      if (
        generationVersion === generation &&
        currentView().record?.testId === record.testId &&
        known?.status !== 'completed' &&
        known?.status !== 'failed' &&
        known?.status !== 'rejected'
      )
        setStopMessage(
          currentView().record?.stage === 'finished'
            ? stopPendingMessage(currentView().record)
            : 'Исход остановки неизвестен; безопасное давление не подтверждено.',
        );
    } finally {
      endCall('stop', record.testId);
      if (!active) pruneStopStatuses();
    }
  }

  function create(
    parameters: Pick<FormParameters, 'valve' | 'area' | 'pmax' | 'measurementSeconds'>,
  ) {
    if (!active) return Promise.resolve();
    const { snapshot } = currentView();
    const service = input.service;
    if (!snapshot) return Promise.resolve();
    return submit('create', null, null, (commandId) => {
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
      return { originalJSON: JSON.stringify(command), send: () => service.createTest(command) };
    });
  }
  function startTest(checks: Pick<StartCommandInput, 'valveClosed' | 'connectionConfirmed'>) {
    if (!active) return Promise.resolve();
    const { record, snapshot } = currentView();
    const service = input.service;
    if (!record || !snapshot) return Promise.resolve();
    return submit('start', record.testId, null, (commandId) => {
      const command = { commandId, expectedRevision: snapshot.revision, ...checks };
      return {
        originalJSON: JSON.stringify(command),
        send: () => service.startTest(record.testId, command),
      };
    });
  }
  function confirmAction() {
    if (!active) return Promise.resolve();
    const { record, snapshot } = currentView();
    const service = input.service;
    const action = snapshot?.pendingAction;
    if (!record || !snapshot || !action) return Promise.resolve();
    return submit('confirm', record.testId, action.actionId, (commandId) => {
      const command = { commandId, expectedRevision: snapshot.revision };
      return {
        originalJSON: JSON.stringify(command),
        send: () => service.confirmAction(record.testId, action.actionId, command),
      };
    });
  }
  function canForget(item: Pending): boolean {
    const current = pending.find((entry) => samePending(entry, item));
    return (
      !!current?.retired &&
      (current.kind !== 'stop' ||
        (countCalls('stop', current.testId ?? '') === 0 &&
          countCalls(
            'stopLookup',
            commandLookupKey(hostRunId, backendRunId, databaseId, current.id),
          ) === 0))
    );
  }
  return {
    getSnapshot: () => snapshot,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    start() {
      if (active) return;
      active = true;
      generationVersion += 1;
      memory = memoryFor(databaseId);
      const restored = restorePending(storageKey, memory, pendingStorage);
      pending = restored ?? (memory.volatile ? restoredMemory(memory.volatile) : []);
      legacy = readLegacyPending();
      legacyReviewed = false;
      storageUnconfirmed = restored === null || memory.storageUnknown;
      volatileStopTracking = memory.volatile
        ? hasUnstoredStop(memory.volatile, storageKey, pendingStorage)
        : false;
      stopCalls = subscribeCalls(() => changed());
      changed(true);
    },
    update(value: CommandInput) {
      const previous = input;
      input = value;
      if (
        previous?.connection !== value.connection ||
        previous?.lastSnapshot !== value.lastSnapshot ||
        previous?.service !== value.service
      )
        changed(true);
    },
    dispose() {
      active = false;
      generationVersion += 1;
      stopCalls?.();
      stopCalls = undefined;
      reconcileNeeded = false;
      pruneStopStatuses();
    },
    create,
    startTest,
    confirmAction,
    stop,
    lookup,
    forget: (item: Pending) => {
      if (active && canForget(item)) forget(item);
    },
    showMessage,
    retryStorage: () => savePending(pending, true),
    readLegacy: () => {
      if (!active) return;
      legacy = readLegacyPending();
      legacyReviewed = false;
      changed(true);
    },
    reviewLegacy: () => {
      if (!active) return;
      legacyReviewed = true;
      changed(true);
    },
    reviewAttempt: (item: Pending) => {
      if (!active) return false;
      const current = pending.find((entry) => samePending(entry, item));
      if (!current?.legacyUnverifiable || current.retired) return false;
      return savePending(
        pending.map((entry) =>
          samePending(entry, current) ? { ...entry, retired: true, legacyReviewed: true } : entry,
        ),
      );
    },
    canForget,
  };
}
export type CommandCoordinator = ReturnType<typeof createCommandCoordinator>;
