import type {
  ConnectionNotice,
  ConnectionState,
  Event as CoreEvent,
  SessionView,
  Snapshot,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { SessionBridge } from './bridge';

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
  if (current.inFlight < 2 && (current.dirty || current.inFlight === current.expired.size)) {
    current.dirty = false;
    current.start();
  } else if (!current.timer)
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
type EventEnable = {
  listeners: Set<(ready: boolean) => void>;
  ready: boolean;
  inFlight: number;
  generation: number;
  needsStart: boolean;
  start: () => void;
};
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
      const started = performance.now();
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
        if (performance.now() - started >= 4000 && attempt > current.lastCompleted) {
          current.lastCompleted = attempt;
          for (const callback of current.listeners) callback(null);
          if (
            notice.connection.hostRunId === current.hostRunId &&
            notice.connection.leaseAcquireUnknown
          ) {
            const terminal = { ...notice, event: null, gap: false, snapshotBoundary: 0 };
            for (const callback of current.listeners) callback(terminal);
          }
          return;
        }
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
    state = {
      listeners: new Set(),
      ready: false,
      inFlight: 0,
      generation: 0,
      needsStart: true,
      start: () => {},
    };
    enabledEvents.set(bridge, state);
    const current = state;
    current.start = () => {
      if (
        !current.listeners.size ||
        !current.needsStart ||
        current.inFlight >= 2 ||
        !bridge.enableConnectionEvents
      )
        return;
      current.needsStart = false;
      current.inFlight += 1;
      const generation = current.generation;
      void bridge
        .enableConnectionEvents()
        .then(
          () => {
            if (generation !== current.generation) return;
            current.ready = true;
            for (const callback of current.listeners) callback(true);
          },
          () => {
            if (generation !== current.generation) return;
            current.ready = false;
            current.needsStart = true;
            for (const callback of current.listeners) callback(false);
            current.listeners.clear();
          },
        )
        .finally(() => {
          current.inFlight -= 1;
          if (current.needsStart && current.listeners.size) current.start();
          if (!current.listeners.size && !current.inFlight && !current.ready)
            enabledEvents.delete(bridge);
        });
    };
  }
  state.listeners.add(listener);
  const current = state;
  if (current.ready)
    queueMicrotask(() => {
      if (current.listeners.has(listener)) listener(true);
    });
  else current.start();
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
      const expiresAt = performance.now() + 4000;
      const deadline = setTimeout(() => notify(null), 4000);
      const check = async (): Promise<SessionResult> => {
        const ticket = await bridge.beginSessionCheck();
        if (
          attempt <= current.lastSettled ||
          current.listeners.size === 0 ||
          performance.now() >= expiresAt
        )
          return null;
        await bridge.keepAlive(ticket);
        if (
          attempt <= current.lastSettled ||
          current.listeners.size === 0 ||
          performance.now() >= expiresAt
        )
          return null;
        const started = performance.now();
        return { view: await bridge.sessionView(), started };
      };
      void check()
        .then(
          (result) => notify(performance.now() >= expiresAt ? null : result),
          () => notify(null),
        )
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

// Invalidating a consumer never frees an underlying operation's slot.
function invalidateReads(bridge: SessionBridge) {
  const check = pendingSessions.get(bridge);
  if (check) check.lastSettled = check.nextAttempt;
  const observation = lastSessionReads.get(bridge);
  if (observation) {
    observation.completed = observation.attempt;
    observation.dirty = true;
  }
  const watcher = connectionWatchers.get(bridge);
  if (watcher) {
    watcher.generation += 1;
    watcher.generationInFlight = 0;
    watcher.lastCompleted = watcher.nextAttempt;
    watcher.revision = 0;
    watcher.sequence = 0;
  }
  const events = enabledEvents.get(bridge);
  if (events) {
    events.generation += 1;
    events.ready = false;
    events.needsStart = true;
  }
}

export type SessionProjection = {
  linkValid: boolean;
  unknownAcquire: boolean;
  connection: { view: SessionView; expiresAt: number } | null;
  lastSnapshot: Snapshot | null;
  gapVersion: number;
  observationVersion: number;
};

type ProjectionListener = (projection: SessionProjection) => void;
type ObservationCall = 'check' | 'observe' | 'wait' | 'enable' | 'connectionAck' | 'coreAck';
let physicalCalls = new Map<ObservationCall, number>();
const ackWaiters = new Set<() => void>();

export class SessionObservation {
  private listeners = new Set<ProjectionListener>();
  private stop: (() => void) | undefined;
  private currentBridge: SessionBridge | undefined;
  private generation = 0;
  private linkDeadline = 0;
  private watchDeadline = 0;
  private pendingLinkDeadline: number | undefined;
  private replacementGapTimer: ReturnType<typeof setTimeout> | undefined;
  private known: { revision: number; sequence: number; measurement: number } | null = null;
  private cursor: {
    revision: number;
    requiredRevision: number;
    requiredSequence: number;
    snapshotSequence: number;
    linkRevision: number;
    linkGeneration: number;
    streamGeneration: number | undefined;
  } = {
    revision: -1,
    requiredRevision: -1,
    requiredSequence: 0,
    snapshotSequence: 0,
    linkRevision: -1,
    linkGeneration: -1,
    streamGeneration: undefined,
  };
  private lateUnknown: ((state: ConnectionState) => void) | undefined;
  private value: SessionProjection = {
    linkValid: false,
    unknownAcquire: false,
    connection: null,
    lastSnapshot: null,
    gapVersion: 0,
    observationVersion: 0,
  };

  constructor(
    readonly hostRunId: string,
    readonly backendRunId: string,
  ) {
    Object.defineProperties(this.proxy, {
      observeSession: {
        get: () =>
          this.currentBridge?.observeSession
            ? () =>
                this.guarded(
                  'observe',
                  (bridge) => bridge.observeSession?.() ?? Promise.reject(new Error('unavailable')),
                  (result) => result.connection,
                )
            : undefined,
      },
      waitConnection: {
        get: () =>
          this.currentBridge?.waitConnection
            ? (revision: number, sequence: number) =>
                this.guarded(
                  'wait',
                  (bridge) =>
                    bridge.waitConnection?.(revision, sequence) ??
                    Promise.reject(new Error('unavailable')),
                  (result) => result.connection,
                )
            : undefined,
      },
      enableConnectionEvents: {
        get: () =>
          this.currentBridge?.enableConnectionEvents
            ? () =>
                this.guarded(
                  'enable',
                  (bridge) =>
                    bridge.enableConnectionEvents?.() ?? Promise.reject(new Error('unavailable')),
                )
            : undefined,
      },
      subscribeConnection: {
        get: () => this.currentBridge?.subscribeConnection?.bind(this.currentBridge),
      },
      subscribeCoreEvent: {
        get: () => this.currentBridge?.subscribeCoreEvent?.bind(this.currentBridge),
      },
      acknowledgeConnection: {
        get: () =>
          this.currentBridge?.acknowledgeConnection
            ? (host: string, revision: number) => {
                this.queueAck('connectionAck', host, revision);
                return Promise.resolve();
              }
            : undefined,
      },
      acknowledgeCoreEvent: {
        get: () =>
          this.currentBridge?.acknowledgeCoreEvent
            ? (backend: string, sequence: number) => {
                this.queueAck('coreAck', backend, sequence);
                return Promise.resolve();
              }
            : undefined,
      },
    });
  }

  private publish() {
    for (const listener of this.listeners) listener(this.getSnapshot());
  }
  getSnapshot(): SessionProjection {
    const now = performance.now();
    const linkValid =
      this.value.linkValid &&
      now < this.linkDeadline &&
      (this.watchDeadline === 0 || now < this.watchDeadline);
    const connection =
      linkValid && this.value.connection && now < this.value.connection.expiresAt
        ? this.value.connection
        : null;
    return linkValid === this.value.linkValid && connection === this.value.connection
      ? this.value
      : { ...this.value, linkValid, connection };
  }
  getSnapshotFor(bridge: SessionBridge): SessionProjection {
    const value = this.getSnapshot();
    return this.currentBridge === bridge ? value : { ...value, linkValid: false, connection: null };
  }
  private remember(view: SessionView): boolean {
    const next = view.snapshot;
    if (view.connection.hostRunId !== this.hostRunId || next?.backendRunId !== this.backendRunId)
      return false;
    const revision = view.connection.revision;
    if (!Number.isSafeInteger(revision) || !Number.isSafeInteger(next.lastEventSequence))
      return false;
    const measurement = Math.max(0, ...(next.measurements ?? []).map((item) => item.sequence));
    const old = this.known;
    if (
      old &&
      (revision < old.revision ||
        next.lastEventSequence < old.sequence ||
        (revision === old.revision &&
          next.lastEventSequence === old.sequence &&
          measurement < old.measurement))
    )
      return false;
    this.known = { revision, sequence: next.lastEventSequence, measurement };
    if (this.value.lastSnapshot && JSON.stringify(this.value.lastSnapshot) !== JSON.stringify(next))
      this.value = { ...this.value, connection: null };
    this.value = {
      ...this.value,
      lastSnapshot: next,
      observationVersion: this.value.observationVersion + 1,
    };
    return true;
  }
  private guarded<T>(
    kind: ObservationCall,
    operation: (bridge: SessionBridge) => Promise<T>,
    connection?: (result: T) => ConnectionState,
  ): Promise<T> {
    const generation = this.generation;
    const bridge = this.currentBridge;
    if (!bridge) return Promise.reject(new Error('session observer stopped'));
    const calls = physicalCalls;
    const active = calls.get(kind) ?? 0;
    if (active >= (kind === 'wait' ? 3 : 2)) return Promise.reject(new Error('session calls busy'));
    calls.set(kind, active + 1);
    let request: Promise<T>;
    try {
      request = operation(bridge);
    } catch (error) {
      request = Promise.reject(error);
    }
    return request
      .then((result) => {
        const state = connection?.(result);
        if (
          state &&
          state.hostRunId === this.hostRunId &&
          state.leaseAcquireUnknown &&
          Number.isSafeInteger(state.revision) &&
          state.revision >= Math.max(0, this.cursor.linkRevision) &&
          Number.isSafeInteger(state.streamGeneration) &&
          state.streamGeneration >= Math.max(0, this.cursor.linkGeneration)
        ) {
          this.value = { ...this.value, unknownAcquire: true, linkValid: false, connection: null };
          this.lateUnknown?.(state);
        }
        if (generation !== this.generation) throw new Error('session observer replaced');
        return result;
      })
      .finally(() => {
        calls.set(kind, (calls.get(kind) ?? 1) - 1);
        if (kind === 'connectionAck' || kind === 'coreAck') {
          for (const wake of ackWaiters) wake();
        }
      });
  }
  private pendingAcks = new Map<'connectionAck' | 'coreAck', { id: string; sequence: number }>();
  private queueAck(kind: 'connectionAck' | 'coreAck', id: string, sequence: number) {
    const pending = this.pendingAcks.get(kind);
    if (pending?.id === id && pending.sequence > sequence) return;
    this.pendingAcks.set(kind, { id, sequence });
    this.flushAcks();
  }
  private flushAcks = () => {
    if (!this.listeners.size || !this.currentBridge) return;
    for (const [kind, value] of this.pendingAcks) {
      if ((physicalCalls.get(kind) ?? 0) >= 2) continue;
      if (
        kind === 'connectionAck'
          ? !this.currentBridge.acknowledgeConnection
          : !this.currentBridge.acknowledgeCoreEvent
      )
        continue;
      this.pendingAcks.delete(kind);
      void this.guarded(kind, (bridge) =>
        kind === 'connectionAck'
          ? (bridge.acknowledgeConnection?.(value.id, value.sequence) ?? Promise.resolve())
          : (bridge.acknowledgeCoreEvent?.(value.id, value.sequence) ?? Promise.resolve()),
      ).catch(() => {});
    }
  };
  private proxy: SessionBridge = {
    beginSessionCheck: () => this.guarded('check', (bridge) => bridge.beginSessionCheck()),
    keepAlive: (ticket) => this.guarded('check', (bridge) => bridge.keepAlive(ticket)),
    sessionView: () =>
      this.guarded(
        'check',
        (bridge) => bridge.sessionView(),
        (result) => result.connection,
      ),
  };

  subscribe(bridge: SessionBridge, listener: ProjectionListener): () => void {
    if (this.currentBridge !== bridge) {
      if (this.value.linkValid) this.pendingLinkDeadline = this.linkDeadline;
      this.stop?.();
      this.stop = undefined;
      this.generation += 1;
      invalidateReads(this.proxy);
      this.value = { ...this.value, linkValid: false, connection: null };
      this.publish();
      this.listeners.clear();
      this.currentBridge = bridge;
    }
    this.listeners.add(listener);
    ackWaiters.add(this.flushAcks);
    this.flushAcks();
    if (this.pendingLinkDeadline !== undefined && !this.replacementGapTimer) {
      const delay = this.pendingLinkDeadline - performance.now();
      if (delay <= 0) {
        this.pendingLinkDeadline = undefined;
        this.value = { ...this.value, gapVersion: this.value.gapVersion + 1 };
        this.publish();
      } else {
        this.replacementGapTimer = setTimeout(() => {
          this.replacementGapTimer = undefined;
          this.pendingLinkDeadline = undefined;
          if (this.listeners.size && !this.value.linkValid) {
            this.value = { ...this.value, gapVersion: this.value.gapVersion + 1 };
            this.publish();
          }
        }, delay);
      }
    }
    if (!this.stop) this.start();
    listener(this.getSnapshot());
    return () => {
      this.listeners.delete(listener);
      if (!this.listeners.size) {
        ackWaiters.delete(this.flushAcks);
        if (this.value.linkValid) this.pendingLinkDeadline = this.linkDeadline;
        clearTimeout(this.replacementGapTimer);
        this.replacementGapTimer = undefined;
        this.stop?.();
        this.stop = undefined;
        this.generation += 1;
        invalidateReads(this.proxy);
        this.currentBridge = undefined;
        this.value = { ...this.value, linkValid: false, connection: null };
      }
    };
  }

  private start() {
    const hostRunId = this.hostRunId;
    const backendRunId = this.backendRunId;
    const bridge = this.proxy;
    const cursor = this.cursor;
    const setLink = (linkValid: boolean, deadline = 0) => {
      if (linkValid) {
        const expired =
          (this.pendingLinkDeadline !== undefined &&
            performance.now() >= this.pendingLinkDeadline) ||
          (this.value.linkValid && performance.now() >= this.linkDeadline);
        if (expired) {
          this.value = {
            ...this.value,
            connection: null,
            gapVersion: this.value.gapVersion + 1,
          };
          this.publish();
        }
        this.linkDeadline = deadline;
        this.pendingLinkDeadline = undefined;
        clearTimeout(this.replacementGapTimer);
        this.replacementGapTimer = undefined;
      }
      this.value = {
        ...this.value,
        linkValid,
        connection: linkValid ? this.value.connection : null,
      };
      this.publish();
    };
    const setUnknown = (unknownAcquire: boolean) => {
      this.value = { ...this.value, unknownAcquire };
      this.publish();
    };
    const reportGapValue = () => {
      this.value = { ...this.value, gapVersion: this.value.gapVersion + 1 };
      this.publish();
    };
    const setView = (view: SessionView | null, expiresAt: number) => {
      const accepted = view && this.remember(view);
      this.value = { ...this.value, connection: accepted ? { view, expiresAt } : null };
      this.publish();
    };
    const rememberLast = (view: SessionView) => {
      if (this.remember(view)) this.publish();
    };
    let mounted = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let expiration: ReturnType<typeof setTimeout> | undefined;
    let linkExpiration: ReturnType<typeof setTimeout> | undefined;
    let unsubscribe: (() => void) | undefined;
    let heartbeatConfirmed = false;
    let terminalAcquire = this.value.unknownAcquire;
    let minimumObservationStart = -1;
    let gapReported = false;
    function reportGap() {
      if (!gapReported) reportGapValue();
      gapReported = true;
    }
    function observeUnknownAcquire(state: ConnectionState): boolean {
      if (
        state.hostRunId === hostRunId &&
        Number.isSafeInteger(state.revision) &&
        state.revision >= Math.max(0, cursor.linkRevision) &&
        Number.isSafeInteger(state.streamGeneration) &&
        state.streamGeneration >= Math.max(0, cursor.linkGeneration) &&
        state.leaseAcquireUnknown &&
        !terminalAcquire
      ) {
        terminalAcquire = true;
        setUnknown(true);
        setLink(false);
        clearTimeout(timer);
        unsubscribe?.();
        unsubscribe = undefined;
        clearTimeout(expiration);
        clearTimeout(linkExpiration);
        setView(null, 0);
        reportGap();
      }
      return terminalAcquire;
    }
    let eventsReady = !bridge.enableConnectionEvents;
    let enablePending = false;
    let stopEnable: (() => void) | undefined;
    let watchReady = !bridge.waitConnection;
    let watchTimeout: ReturnType<typeof setTimeout> | undefined;
    this.watchDeadline = 0;
    const owner = this;
    function expireWatch() {
      if (owner.watchDeadline > 0 && performance.now() >= owner.watchDeadline && watchReady) {
        watchReady = false;
        setLink(false);
        setView(null, 0);
        reportGap();
      }
    }
    setLink(false);
    setUnknown(terminalAcquire);
    setView(null, 0);
    const stopEvents = bridge.subscribeConnection?.((state) => {
      if (!mounted || state.hostRunId !== hostRunId || !Number.isSafeInteger(state.revision))
        return;
      observeUnknownAcquire(state);
      if (state.revision >= cursor.linkRevision && Number.isSafeInteger(state.streamGeneration)) {
        if (
          !state.linkValid ||
          (cursor.linkGeneration >= 0 && state.streamGeneration > cursor.linkGeneration)
        ) {
          setLink(false);
          reportGap();
        }
        cursor.linkRevision = state.revision;
        cursor.linkGeneration = Math.max(cursor.linkGeneration, state.streamGeneration);
      }
      if (state.revision > cursor.revision) {
        cursor.requiredRevision = Math.max(cursor.requiredRevision, state.revision);
        setView(null, 0);
        if (!terminalAcquire && state.status === 'live') requestSessionObservation(bridge);
      }
      void bridge.acknowledgeConnection?.(state.hostRunId, state.revision).catch(() => {});
    });
    function coreEvent(event: CoreEvent) {
      if (
        event.backendRunId !== backendRunId ||
        !Number.isSafeInteger(event.sequence) ||
        event.sequence < 1
      )
        return;
      if (event.sequence > cursor.snapshotSequence) {
        cursor.requiredSequence = Math.max(cursor.requiredSequence, event.sequence);
        setView(null, 0);
        requestSessionObservation(bridge);
      }
    }
    const stopCoreEvents = bridge.subscribeCoreEvent?.((event) => {
      if (!mounted) return;
      coreEvent(event);
      if (event.backendRunId === backendRunId && Number.isSafeInteger(event.sequence))
        void bridge.acknowledgeCoreEvent?.(event.backendRunId, event.sequence).catch(() => {});
    });
    const stopWatch = watchConnection(bridge, hostRunId, backendRunId, (notice) => {
      if (!mounted) return;
      expireWatch();
      clearTimeout(watchTimeout);
      const state = notice?.connection;
      if (state) observeUnknownAcquire(state);
      if (notice?.event) coreEvent(notice.event);
      if (
        notice?.gap &&
        Number.isSafeInteger(notice.snapshotBoundary) &&
        notice.snapshotBoundary > cursor.snapshotSequence
      ) {
        cursor.requiredSequence = Math.max(cursor.requiredSequence, notice.snapshotBoundary);
        setView(null, 0);
      }
      watchReady = !!state && state.hostRunId === hostRunId && Number.isSafeInteger(state.revision);
      if (!watchReady) {
        setLink(false);
        reportGap();
      } else if (
        state &&
        state.revision >= cursor.linkRevision &&
        Number.isSafeInteger(state.streamGeneration)
      ) {
        if (
          !state.linkValid ||
          (cursor.linkGeneration >= 0 && state.streamGeneration > cursor.linkGeneration)
        ) {
          setLink(false);
          reportGap();
        }
        cursor.linkRevision = state.revision;
        cursor.linkGeneration = Math.max(cursor.linkGeneration, state.streamGeneration);
      }
      if (!watchReady || (state && state.revision > cursor.revision)) {
        if (watchReady && state && Number.isSafeInteger(state.revision))
          cursor.requiredRevision = Math.max(cursor.requiredRevision, state.revision);
        setView(null, 0);
        if (!terminalAcquire && watchReady && state?.status === 'live')
          requestSessionObservation(bridge);
      }
      owner.watchDeadline = performance.now() + 5000;
      watchTimeout = setTimeout(() => {
        watchReady = false;
        if (mounted) {
          reportGap();
          setLink(false);
          setView(null, 0);
        }
      }, 5000);
    });
    function ensureEvents() {
      if (enablePending) return;
      enablePending = true;
      stopEnable?.();
      stopEnable = observeConnectionEvents(bridge, (ready) => {
        enablePending = false;
        eventsReady = ready;
        if (!ready && mounted && !bridge.waitConnection) {
          setLink(false);
          setView(null, 0);
        }
      });
    }
    function applyResult(result: { view: SessionView; started: number } | null) {
      if (!mounted) return;
      expireWatch();
      if (terminalAcquire || (result && observeUnknownAcquire(result.view.connection))) {
        setLink(false);
        setView(null, 0);
        return;
      }
      if (result && result.view.connection.revision < cursor.revision) return;
      if (result) {
        const { view, started } = result;
        const state = view.connection;
        const remaining = view.linkValidForMs - (performance.now() - started);
        if (
          state.hostRunId === hostRunId &&
          Number.isSafeInteger(state.revision) &&
          Number.isSafeInteger(state.streamGeneration) &&
          state.revision >= cursor.linkRevision &&
          state.streamGeneration >= cursor.linkGeneration
        ) {
          if (cursor.linkGeneration >= 0 && state.streamGeneration > cursor.linkGeneration)
            reportGap();
          cursor.linkRevision = state.revision;
          cursor.linkGeneration = state.streamGeneration;
          const valid =
            state.linkValid &&
            remaining > 0 &&
            (eventsReady || !!bridge.waitConnection) &&
            watchReady;
          setLink(valid, started + view.linkValidForMs);
          clearTimeout(linkExpiration);
          if (valid)
            linkExpiration = setTimeout(() => {
              if (mounted) {
                setLink(false);
                reportGap();
              }
            }, remaining);
          else if (!state.linkValid || remaining <= 0) reportGap();
        }
      } else {
        setLink(false);
        reportGap();
      }
      if (
        result &&
        (eventsReady || !!bridge.waitConnection) &&
        watchReady &&
        result.view.connection.revision >= cursor.requiredRevision &&
        Number.isSafeInteger(result.view.connection.streamGeneration) &&
        result.view.connection.streamGeneration >= cursor.linkGeneration &&
        result.view.connection.revision >= cursor.linkRevision &&
        result.view.snapshot &&
        Number.isSafeInteger(result.view.snapshot.lastEventSequence) &&
        result.view.snapshot.lastEventSequence >= cursor.requiredSequence
      ) {
        const { view, started } = result;
        const remaining = view.validForMs - (performance.now() - started);
        if (
          view.connection.hostRunId !== hostRunId ||
          !Number.isSafeInteger(view.connection.revision) ||
          view.connection.revision < cursor.revision
        ) {
          setView(null, 0);
        } else {
          if (
            cursor.streamGeneration !== undefined &&
            view.connection.streamGeneration > cursor.streamGeneration
          )
            reportGap();
          cursor.streamGeneration = Math.max(
            cursor.streamGeneration ?? 0,
            view.connection.streamGeneration,
          );
          cursor.revision = view.connection.revision;
          const live =
            view.connection.status === 'live' &&
            view.snapshot?.backendRunId === backendRunId &&
            remaining > 0;
          if (live) {
            cursor.snapshotSequence = view.snapshot?.lastEventSequence ?? 0;
            markSnapshotSequence(bridge, hostRunId, backendRunId, cursor.snapshotSequence);
          }
          if (view.connection.linkValid) gapReported = false;
          setView(live ? view : null, live ? started + view.validForMs : 0);
          clearTimeout(expiration);
          if (live)
            expiration = setTimeout(() => {
              if (mounted) {
                setView(null, 0);
              }
            }, remaining);
        }
      } else {
        setView(null, 0);
      }
    }
    const stopObserving = readLastSession(
      bridge,
      (view, started) => {
        if (!mounted) return;
        rememberLast(view);
        if (observeUnknownAcquire(view.connection)) return;
        if (heartbeatConfirmed && started > minimumObservationStart) applyResult({ view, started });
        else if (
          !heartbeatConfirmed &&
          view.connection.hostRunId === hostRunId &&
          Number.isSafeInteger(view.connection.revision) &&
          Number.isSafeInteger(view.connection.streamGeneration) &&
          view.connection.revision > cursor.linkRevision
        ) {
          cursor.linkRevision = view.connection.revision;
          cursor.linkGeneration = Math.max(cursor.linkGeneration, view.connection.streamGeneration);
        }
      },
      (view) => {
        if (mounted) observeUnknownAcquire(view.connection);
      },
    );
    function read() {
      if (!mounted || terminalAcquire) return;
      if (!eventsReady) ensureEvents();
      unsubscribe = readSession(bridge, (result) => {
        if (!mounted || terminalAcquire) return;
        if (result) heartbeatConfirmed = true;
        else {
          heartbeatConfirmed = false;
          minimumObservationStart = performance.now();
        }
        applyResult(result);
        if (!terminalAcquire) timer = setTimeout(read, 2000);
      });
    }
    read();
    this.lateUnknown = observeUnknownAcquire;
    const stop = () => {
      this.lateUnknown = undefined;
      mounted = false;
      clearTimeout(timer);
      clearTimeout(expiration);
      clearTimeout(linkExpiration);
      clearTimeout(watchTimeout);
      unsubscribe?.();
      stopEvents?.();
      stopCoreEvents?.();
      stopWatch();
      stopObserving();
      stopEnable?.();
    };
    this.stop = stop;
  }
}

const observations = new Map<string, SessionObservation>();
export function sessionObservation(hostRunId: string, backendRunId: string): SessionObservation {
  const key = JSON.stringify([hostRunId, backendRunId]);
  let observer = observations.get(key);
  if (!observer) {
    observer = new SessionObservation(hostRunId, backendRunId);
    observations.set(key, observer);
  }
  return observer;
}

// Test cases model independent app processes, including deliberately never-settled calls.
export function resetObservationsForTests() {
  observations.clear();
  physicalCalls = new Map();
  ackWaiters.clear();
}
