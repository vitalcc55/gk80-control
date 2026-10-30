import type {
  ConnectionNotice,
  ConnectionState,
  Event as CoreEvent,
  DesktopStatus,
  SessionView,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

export interface DesktopBridge {
  status(): Promise<DesktopStatus>;
  verify(): Promise<DesktopStatus>;
  subscribe(callback: (status: DesktopStatus) => void): () => void;
}

export interface SessionBridge {
  beginSessionCheck(): Promise<string>;
  keepAlive(ticket: string): Promise<void>;
  sessionView(): Promise<SessionView>;
  observeSession?(): Promise<SessionView>;
  subscribeConnection?(callback: (state: ConnectionState) => void): () => void;
  enableConnectionEvents?(): Promise<void>;
  acknowledgeConnection?(host: string, revision: number): Promise<void>;
  subscribeCoreEvent?(callback: (event: CoreEvent) => void): () => void;
  acknowledgeCoreEvent?(backend: string, sequence: number): Promise<void>;
  waitConnection?(after: number, afterSequence: number): Promise<ConnectionNotice>;
}

type SessionResult = { view: SessionView; started: number } | null;
type SessionListener = (result: SessionResult) => void;
type SessionCheck = {
  listeners: Set<SessionListener>;
  inFlight: number;
  nextAttempt: number;
  lastSettled: number;
  start: () => void;
};
const pendingSessions = new WeakMap<SessionBridge, SessionCheck>();
type LastSessionRead = {
  listeners: Set<{
    onView: (view: SessionView, started: number) => void;
    onStaleUnknown?: (view: SessionView) => void;
  }>;
  inFlight: number;
  expired: Set<number>;
  timer: ReturnType<typeof setTimeout> | undefined;
  attempt: number;
  completed: number;
  dirty: boolean;
  start: () => void;
};
const lastSessionReads = new WeakMap<SessionBridge, LastSessionRead>();

// Read-only fallback for the reserved Stop ID; it does not confirm UI liveness.
export function readLastSession(
  bridge: SessionBridge,
  listener: (view: SessionView, started: number) => void,
  onStaleUnknown?: (view: SessionView) => void,
): () => void {
  if (!bridge.observeSession) return () => {};
  let pending = lastSessionReads.get(bridge);
  if (!pending) {
    pending = {
      listeners: new Set(),
      inFlight: 0,
      expired: new Set(),
      timer: undefined,
      attempt: 0,
      completed: 0,
      dirty: false,
      start: () => {},
    };
    lastSessionReads.set(bridge, pending);
    const current = pending;
    const schedule = (delay: number) => {
      if (!current.listeners.size || current.timer) return;
      current.timer = setTimeout(() => {
        current.timer = undefined;
        current.start();
      }, delay);
    };
    current.start = () => {
      if (!current.listeners.size || current.inFlight >= 2 || !bridge.observeSession) return;
      current.inFlight += 1;
      const attempt = ++current.attempt;
      const started = performance.now();
      const watchdog = setTimeout(() => {
        current.expired.add(attempt);
        schedule(0);
      }, 4000);
      void bridge
        .observeSession()
        .then(
          (view) => {
            if (attempt <= current.completed) {
              if (view.connection.leaseAcquireUnknown) {
                for (const callback of current.listeners) callback.onStaleUnknown?.(view);
              }
              return;
            }
            current.completed = attempt;
            for (const callback of current.listeners) callback.onView(view, started);
          },
          () => {
            current.completed = Math.max(current.completed, attempt);
          },
        )
        .finally(() => {
          clearTimeout(watchdog);
          current.expired.delete(attempt);
          current.inFlight -= 1;
          if (current.listeners.size) {
            const delay = current.dirty ? 0 : 2000;
            current.dirty = false;
            schedule(delay);
          } else if (current.inFlight === 0) lastSessionReads.delete(bridge);
        });
    };
  }
  const current = pending;
  const subscriber = onStaleUnknown ? { onView: listener, onStaleUnknown } : { onView: listener };
  current.listeners.add(subscriber);
  if (current.inFlight < 2 && current.inFlight === current.expired.size) current.start();
  else if (!current.timer)
    current.timer = setTimeout(() => {
      current.timer = undefined;
      current.start();
    }, 4000);
  return () => {
    current.listeners.delete(subscriber);
    if (!current.listeners.size) {
      clearTimeout(current.timer);
      current.timer = undefined;
      if (!current.inFlight) lastSessionReads.delete(bridge);
    }
  };
}

export function requestSessionObservation(bridge: SessionBridge): void {
  const pending = lastSessionReads.get(bridge);
  if (!pending?.listeners.size) return;
  if (pending.inFlight && (pending.inFlight >= 2 || pending.expired.size < pending.inFlight)) {
    pending.dirty = true;
    return;
  }
  clearTimeout(pending.timer);
  pending.timer = undefined;
  pending.start();
}
type EventEnable = { listeners: Set<(ready: boolean) => void>; ready: boolean };
const enabledEvents = new WeakMap<SessionBridge, EventEnable>();
type Watcher = {
  listeners: Set<(notice: ConnectionNotice | null) => void>;
  hostRunId: string;
  backendRunId: string;
  generation: number;
  generationInFlight: number;
  revision: number;
  sequence: number;
  inFlight: number;
  nextAttempt: number;
  lastCompleted: number;
  timer: ReturnType<typeof setTimeout> | undefined;
  start: () => void;
};
const connectionWatchers = new WeakMap<SessionBridge, Watcher>();

export function markSnapshotSequence(
  bridge: SessionBridge,
  hostRunId: string,
  backendRunId: string,
  sequence: number,
): void {
  const watcher = connectionWatchers.get(bridge);
  if (
    watcher?.hostRunId === hostRunId &&
    watcher.backendRunId === backendRunId &&
    Number.isSafeInteger(sequence) &&
    sequence > watcher.sequence
  )
    watcher.sequence = sequence;
}

// One bounded wait is shared across React remounts; a failed wait never confirms freshness.
export function watchConnection(
  bridge: SessionBridge,
  hostRunId: string,
  backendRunId: string,
  listener: (notice: ConnectionNotice | null) => void,
): () => void {
  if (!bridge.waitConnection) return () => {};
  let watcher = connectionWatchers.get(bridge);
  if (!watcher) {
    watcher = {
      listeners: new Set(),
      hostRunId,
      backendRunId,
      generation: 0,
      generationInFlight: 0,
      revision: 0,
      sequence: 0,
      inFlight: 0,
      nextAttempt: 0,
      lastCompleted: 0,
      timer: undefined,
      start: () => {},
    };
    connectionWatchers.set(bridge, watcher);
    const current = watcher;
    const schedule = (delay: number) => {
      if (!current.listeners.size || current.timer !== undefined) return;
      current.timer = setTimeout(() => {
        current.timer = undefined;
        current.start();
      }, delay);
    };
    const run = async () => {
      if (current.listeners.size === 0 || !bridge.waitConnection) {
        if (current.inFlight === 0) connectionWatchers.delete(bridge);
        return;
      }
      if (current.inFlight >= 3 || current.generationInFlight >= 2) {
        for (const callback of current.listeners) callback(null);
        return;
      }
      current.inFlight += 1;
      current.generationInFlight += 1;
      const attempt = ++current.nextAttempt;
      const generation = current.generation;
      const watchdog = setTimeout(() => {
        if (
          generation === current.generation &&
          attempt > current.lastCompleted &&
          current.listeners.size
        ) {
          for (const callback of current.listeners) callback(null);
          schedule(0);
        }
      }, 4000);
      try {
        const notice = await bridge.waitConnection(current.revision, current.sequence);
        if (generation !== current.generation) return;
        if (attempt < current.lastCompleted) {
          if (
            notice.connection.hostRunId === current.hostRunId &&
            notice.connection.leaseAcquireUnknown
          ) {
            const terminal = { ...notice, event: null, gap: false, snapshotBoundary: 0 };
            for (const callback of current.listeners) callback(terminal);
          }
          return;
        }
        current.lastCompleted = attempt;
        if (notice.connection.hostRunId !== current.hostRunId) {
          for (const callback of current.listeners) callback(null);
          return;
        }
        if (Number.isSafeInteger(notice.connection.revision) && notice.connection.revision >= 0)
          current.revision = Math.max(current.revision, notice.connection.revision);
        if (
          notice.event &&
          notice.event.backendRunId === current.backendRunId &&
          Number.isSafeInteger(notice.event.sequence) &&
          notice.event.sequence > current.sequence
        )
          current.sequence = notice.event.sequence;
        for (const callback of current.listeners) callback(notice);
      } catch {
        if (generation === current.generation && attempt > current.lastCompleted) {
          current.lastCompleted = attempt;
          for (const callback of current.listeners) callback(null);
        }
      } finally {
        clearTimeout(watchdog);
        current.inFlight -= 1;
        if (generation === current.generation) current.generationInFlight -= 1;
        if (current.listeners.size) schedule(500);
        else if (current.inFlight === 0) connectionWatchers.delete(bridge);
      }
    };
    current.start = () => {
      void run();
    };
  }
  const changedIdentity = watcher.hostRunId !== hostRunId || watcher.backendRunId !== backendRunId;
  if (changedIdentity) {
    watcher.hostRunId = hostRunId;
    watcher.backendRunId = backendRunId;
    watcher.generation += 1;
    watcher.generationInFlight = 0;
    watcher.revision = 0;
    watcher.sequence = 0;
    watcher.lastCompleted = watcher.nextAttempt;
    clearTimeout(watcher.timer);
    watcher.timer = undefined;
  }
  watcher.listeners.add(listener);
  const current = watcher;
  if (current.timer === undefined)
    current.timer = setTimeout(
      () => {
        current.timer = undefined;
        current.start();
      },
      changedIdentity ? 0 : current.inFlight ? 4000 : 0,
    );
  return () => {
    current.listeners.delete(listener);
    if (!current.listeners.size && current.timer !== undefined) {
      clearTimeout(current.timer);
      current.timer = undefined;
    }
  };
}

export function observeConnectionEvents(
  bridge: SessionBridge,
  listener: (ready: boolean) => void,
): () => void {
  if (!bridge.enableConnectionEvents) {
    queueMicrotask(() => listener(true));
    return () => {};
  }
  let state = enabledEvents.get(bridge);
  if (!state) {
    state = { listeners: new Set(), ready: false };
    enabledEvents.set(bridge, state);
    const current = state;
    void bridge.enableConnectionEvents().then(
      () => {
        current.ready = true;
        for (const callback of current.listeners) callback(true);
      },
      () => {
        enabledEvents.delete(bridge);
        for (const callback of current.listeners) callback(false);
        current.listeners.clear();
      },
    );
  }
  state.listeners.add(listener);
  const current = state;
  if (current.ready)
    queueMicrotask(() => {
      if (current.listeners.has(listener)) listener(true);
    });
  return () => {
    current.listeners.delete(listener);
  };
}
// Bound unresolved Wails calls, including when an earlier response never reaches the page.
export function readSession(bridge: SessionBridge, listener: SessionListener): () => void {
  let state = pendingSessions.get(bridge);
  if (!state) {
    state = { listeners: new Set(), inFlight: 0, nextAttempt: 0, lastSettled: 0, start: () => {} };
    pendingSessions.set(bridge, state);
    const current = state;
    current.start = () => {
      if (
        !current.listeners.size ||
        !(
          current.inFlight === 0 ||
          (current.inFlight === 1 && current.lastSettled >= current.nextAttempt)
        )
      )
        return;
      current.inFlight += 1;
      const attempt = ++current.nextAttempt;
      const notify = (value: SessionResult) => {
        if (attempt <= current.lastSettled) return;
        current.lastSettled = attempt;
        const listeners = [...current.listeners];
        current.listeners.clear();
        for (const callback of listeners) callback(value);
      };
      const deadline = setTimeout(() => notify(null), 4000);
      const check = async (): Promise<SessionResult> => {
        const ticket = await bridge.beginSessionCheck();
        if (attempt <= current.lastSettled || current.listeners.size === 0) return null;
        await bridge.keepAlive(ticket);
        if (attempt <= current.lastSettled || current.listeners.size === 0) return null;
        const started = performance.now();
        return { view: await bridge.sessionView(), started };
      };
      void check()
        .then(notify, () => notify(null))
        .finally(() => {
          clearTimeout(deadline);
          current.inFlight -= 1;
          if (current.inFlight === 0 && current.listeners.size === 0)
            pendingSessions.delete(bridge);
          else if (current.listeners.size) current.start();
        });
    };
  }
  const current = state;
  current.listeners.add(listener);
  current.start();
  return () => {
    current.listeners.delete(listener);
  };
}
