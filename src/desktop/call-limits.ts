import { useSyncExternalStore } from 'react';

type Kind = 'stop' | 'lookup';
const calls: Record<Kind, Map<string, number>> = {
  stop: new Map(),
  lookup: new Map(),
};
const listeners = new Set<() => void>();
let revision = 0;

function changed() {
  revision += 1;
  for (const listener of listeners) listener();
}

export function beginCall(kind: Kind, id: string, limit: number): boolean {
  if (countCalls(kind, id) >= limit || totalCalls(kind) >= 64) return false;
  calls[kind].set(id, countCalls(kind, id) + 1);
  changed();
  return true;
}

export function endCall(kind: Kind, id: string): void {
  const remaining = countCalls(kind, id) - 1;
  if (remaining > 0) calls[kind].set(id, remaining);
  else calls[kind].delete(id);
  changed();
}

export function countCalls(kind: Kind, id: string): number {
  return calls[kind].get(id) ?? 0;
}

export function totalCalls(kind: Kind): number {
  return [...calls[kind].values()].reduce((sum, value) => sum + value, 0);
}

export function useCallRevision(): void {
  useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => revision,
  );
}
