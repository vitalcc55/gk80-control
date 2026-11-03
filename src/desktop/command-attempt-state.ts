import type {
  CommandStatus,
  Snapshot,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

export type Pending = {
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
  observationVersion?: string | undefined;
};
export function samePending(left: Pending, right: Pending): boolean {
  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.testId === right.testId &&
    left.actionId === right.actionId &&
    left.originalJSON === right.originalJSON
  );
}
export function pendingKey(item: Pending): string {
  return JSON.stringify([item.id, item.kind, item.testId, item.actionId, item.originalJSON]);
}
export function mergePending(
  stored: Pending[],
  memory: Pending[],
  baseline: Pending[] | null,
  retained: Pending[],
): Pending[] {
  const merged = [...stored];
  const matched = new Set<number>();
  const matchedBaseline = new Set<number>();
  for (const item of memory) {
    const index = stored.findIndex((current, i) => !matched.has(i) && samePending(current, item));
    const baselineIndex = baseline?.findIndex(
      (current, i) => !matchedBaseline.has(i) && samePending(current, item),
    );
    if (baselineIndex !== undefined && baselineIndex >= 0) matchedBaseline.add(baselineIndex);
    if (index < 0) {
      if (
        baselineIndex === undefined ||
        baselineIndex < 0 ||
        retained.some((entry) => samePending(entry, item))
      )
        merged.push(item);
    } else {
      matched.add(index);
      const original =
        baselineIndex === undefined || baselineIndex < 0 ? null : baseline?.[baselineIndex];
      const current = stored[index];
      merged[index] =
        current &&
        (original
          ? observationChanged(original, current)
          : current.observationVersion !== item.observationVersion) &&
        !retained.some((entry) => samePending(entry, item))
          ? {
              ...item,
              retired: current.retired,
              legacyReviewed: current.legacyReviewed,
              observationVersion: current.observationVersion,
            }
          : item;
    }
  }
  return merged;
}
export function observationChanged(left: Pending, right: Pending): boolean {
  return (
    left.retired !== right.retired ||
    left.legacyReviewed !== right.legacyReviewed ||
    left.observationVersion !== right.observationVersion
  );
}
export function stopNoticeKey(
  id: string,
  testId: string | null,
  originalJSON: string | null,
): string {
  return JSON.stringify([id, 'stop', testId, originalJSON]);
}
export function stopLookupNoticeKey(
  id: string,
  testId: string | null,
  originalJSON: string | null,
): string {
  return JSON.stringify([id, 'stop-lookup', testId, originalJSON]);
}
export function stopAttemptNoticeKey(
  id: string,
  testId: string | null,
  originalJSON: string | null,
): string {
  return JSON.stringify([id, 'stop-attempt', testId, originalJSON]);
}
export function hasUnstoredStop(values: Pending[], storageKey: string, storage?: Storage): boolean {
  const stored = readStoredPending(storageKey, storage);
  return values.some(
    (item) => item.kind === 'stop' && !stored?.some((current) => samePending(current, item)),
  );
}
export type CommandFailure = {
  id: string;
  kind: Exclude<Pending['kind'], 'stop'>;
  testId: string | null;
  text: string;
};
export const legacyStorageKey = 'gk80.demo.pending-command';
export type PendingMemory = {
  volatile: Pending[] | null;
  baseline: Pending[] | null;
  removals: Pending[];
  retained: Pending[];
  statuses: Map<string, CommandStatus>;
  storageUnknown: boolean;
};
export const pendingMemory = new Map<string, PendingMemory>();
export function acknowledgeStored(memory: PendingMemory, stored: Pending[]): void {
  memory.removals = memory.removals.filter((item) => {
    const old = memory.baseline?.find((entry) => samePending(entry, item));
    const current = stored.find((entry) => samePending(entry, item));
    if (!memory.baseline) return true;
    if (!current) return !old;
    if (!old) return false;
    return !observationChanged(old, current);
  });
  memory.retained = memory.retained.filter((item) => {
    const old = memory.baseline?.find((entry) => samePending(entry, item));
    const current = stored.find((entry) => samePending(entry, item));
    if (!current) return true;
    if (item.observationVersion && current.observationVersion === item.observationVersion)
      return false;
    if (!memory.baseline) return true;
    return !!old && !observationChanged(old, current);
  });
}
export function pruneStatuses(memory: PendingMemory, items: Pending[]): void {
  const keys = new Set(items.map(pendingKey));
  for (const key of memory.statuses.keys()) if (!keys.has(key)) memory.statuses.delete(key);
}
export function memoryFor(databaseId: string): PendingMemory {
  let memory = pendingMemory.get(databaseId);
  if (!memory) {
    memory = {
      volatile: null,
      baseline: null,
      removals: [],
      retained: [],
      statuses: new Map(),
      storageUnknown: false,
    };
    pendingMemory.set(databaseId, memory);
  }
  return memory;
}
export function commandLookupKey(
  host: string,
  backend: string,
  database: string,
  id: string,
): string {
  return JSON.stringify([host, backend, database, id]);
}
export function mergeStatus(previous: CommandStatus | null, next: CommandStatus): CommandStatus {
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

export function terminalStatus(status: CommandStatus | null): boolean {
  return (
    status?.status === 'completed' || status?.status === 'rejected' || status?.status === 'failed'
  );
}

export function validUUID(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value) &&
    value !== '00000000-0000-0000-0000-000000000000'
  );
}

export function confirmedCommandStatus(
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

export function validExactEnvelope(value: unknown): value is { outcome: string; command: unknown } {
  return (
    typeof value === 'object' &&
    value !== null &&
    'outcome' in value &&
    typeof value.outcome === 'string' &&
    'command' in value &&
    Object.keys(value).every((key) => key === 'outcome' || key === 'command')
  );
}

export function validDispatchEnvelope(
  value: unknown,
): value is { delivery: string; command: unknown } {
  return (
    typeof value === 'object' &&
    value !== null &&
    'delivery' in value &&
    typeof value.delivery === 'string' &&
    'command' in value &&
    Object.keys(value).every((key) => key === 'delivery' || key === 'command')
  );
}

export function commandsBlock(
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

export function parsePending(raw: unknown, scoped = true): Pending | null {
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
  if ('observationVersion' in raw && !validUUID(raw.observationVersion)) return null;
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
    ...('observationVersion' in raw && typeof raw.observationVersion === 'string'
      ? { observationVersion: raw.observationVersion }
      : {}),
    actionId,
  };
}
export function readStoredPending(storageKey: string, storage?: Storage): Pending[] | null {
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
export function restorePending(
  storageKey: string,
  memory: PendingMemory,
  storage?: Storage,
): Pending[] | null {
  const stored = readStoredPending(storageKey, storage);
  if (memory.volatile) {
    memory.storageUnknown = true;
    if (stored === null) return null;
    acknowledgeStored(memory, stored);
    memory.volatile = mergePending(
      stored,
      restoredMemory(memory.volatile),
      memory.baseline,
      memory.retained,
    );
    memory.baseline = stored;
    pruneStatuses(memory, memory.volatile);
    return memory.volatile;
  }
  if (stored === null) memory.storageUnknown = true;
  else {
    memory.baseline = stored;
    pruneStatuses(memory, stored);
  }
  return (
    stored?.map((item) => ({ ...item, status: memory.statuses.get(pendingKey(item)) ?? null })) ??
    null
  );
}

export function restoredMemory(items: Pending[]): Pending[] {
  return items.map((item) => ({
    ...item,
    status: item.originalJSON !== null ? item.status : null,
    receiptRequired: true,
    legacyUnverifiable: item.originalJSON === null,
  }));
}

export function readLegacyPending(): { items: Pending[]; unreadable: boolean } {
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
