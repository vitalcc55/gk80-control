import { useEffect, useState } from 'react';
import type {
  Event as CoreEvent,
  SessionView,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import {
  markSnapshotSequence,
  observeConnectionEvents,
  readLastSession,
  readSession,
  requestSessionObservation,
  type SessionBridge,
  watchConnection,
} from './bridge';

export function SessionStatus({
  bridge,
  hostRunId,
  backendRunId,
  onView,
  onGap,
  snapshotAccepted = true,
}: {
  bridge: SessionBridge;
  hostRunId: string;
  backendRunId: string;
  onView?: (view: SessionView | null, expiresAt: number) => void;
  onGap?: () => void;
  snapshotAccepted?: boolean;
}) {
  const [confirmed, setConfirmed] = useState<{
    bridge: SessionBridge;
    hostRunId: string;
    backendRunId: string;
  } | null>(null);

  useEffect(() => {
    let mounted = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let expiration: ReturnType<typeof setTimeout> | undefined;
    let linkExpiration: ReturnType<typeof setTimeout> | undefined;
    let unsubscribe: (() => void) | undefined;
    let revision = -1;
    let requiredRevision = -1;
    let requiredSequence = 0;
    let snapshotSequence = 0;
    let heartbeatConfirmed = false;
    let minimumObservationStart = -1;
    let linkRevision = -1;
    let linkGeneration = -1;
    let gapReported = false;
    let streamGeneration: number | undefined;
    function reportGap() {
      if (!gapReported) onGap?.();
      gapReported = true;
    }
    let eventsReady = !bridge.enableConnectionEvents;
    let enablePending = false;
    let stopEnable: (() => void) | undefined;
    let watchReady = !bridge.waitConnection;
    let watchTimeout: ReturnType<typeof setTimeout> | undefined;
    setConfirmed(null);
    onView?.(null, 0);
    const stopEvents = bridge.subscribeConnection?.((state) => {
      if (!mounted || state.hostRunId !== hostRunId || !Number.isSafeInteger(state.revision))
        return;
      if (state.revision >= linkRevision && Number.isSafeInteger(state.streamGeneration)) {
        if (!state.linkValid || (linkGeneration >= 0 && state.streamGeneration > linkGeneration)) {
          setConfirmed(null);
          reportGap();
        }
        linkRevision = state.revision;
        linkGeneration = Math.max(linkGeneration, state.streamGeneration);
      }
      if (state.revision > revision) {
        requiredRevision = Math.max(requiredRevision, state.revision);
        onView?.(null, 0);
        if (state.status === 'live') requestSessionObservation(bridge);
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
      if (event.sequence > snapshotSequence) {
        requiredSequence = Math.max(requiredSequence, event.sequence);
        onView?.(null, 0);
        requestSessionObservation(bridge);
      }
    }
    const stopCoreEvents = bridge.subscribeCoreEvent?.((event) => {
      if (!mounted) return;
      coreEvent(event);
      if (event.backendRunId === backendRunId && Number.isSafeInteger(event.sequence))
        void bridge.acknowledgeCoreEvent?.(event.backendRunId, event.sequence).catch(() => {});
    });
    const stopWatch = watchConnection(bridge, (notice) => {
      if (!mounted) return;
      clearTimeout(watchTimeout);
      const state = notice?.connection;
      if (notice?.event) coreEvent(notice.event);
      if (
        notice?.gap &&
        Number.isSafeInteger(notice.snapshotBoundary) &&
        notice.snapshotBoundary > snapshotSequence
      ) {
        requiredSequence = Math.max(requiredSequence, notice.snapshotBoundary);
        onView?.(null, 0);
      }
      watchReady = !!state && state.hostRunId === hostRunId && Number.isSafeInteger(state.revision);
      if (!watchReady) {
        setConfirmed(null);
        reportGap();
      } else if (
        state &&
        state.revision >= linkRevision &&
        Number.isSafeInteger(state.streamGeneration)
      ) {
        if (!state.linkValid || (linkGeneration >= 0 && state.streamGeneration > linkGeneration)) {
          setConfirmed(null);
          reportGap();
        }
        linkRevision = state.revision;
        linkGeneration = Math.max(linkGeneration, state.streamGeneration);
      }
      if (!watchReady || (state && state.revision > revision)) {
        if (state && Number.isSafeInteger(state.revision))
          requiredRevision = Math.max(requiredRevision, state.revision);
        onView?.(null, 0);
        if (watchReady && state?.status === 'live') requestSessionObservation(bridge);
      }
      watchTimeout = setTimeout(() => {
        watchReady = false;
        if (mounted) {
          reportGap();
          setConfirmed(null);
          onView?.(null, 0);
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
          setConfirmed(null);
          onView?.(null, 0);
        }
      });
    }
    function applyResult(result: { view: SessionView; started: number } | null) {
      if (!mounted) return;
      if (result && result.view.connection.revision < revision) return;
      if (result) {
        const { view, started } = result;
        const state = view.connection;
        const remaining = view.linkValidForMs - (performance.now() - started);
        if (
          state.hostRunId === hostRunId &&
          Number.isSafeInteger(state.revision) &&
          Number.isSafeInteger(state.streamGeneration) &&
          state.revision >= linkRevision &&
          state.streamGeneration >= linkGeneration
        ) {
          if (linkGeneration >= 0 && state.streamGeneration > linkGeneration) reportGap();
          linkRevision = state.revision;
          linkGeneration = state.streamGeneration;
          const valid =
            state.linkValid &&
            remaining > 0 &&
            (eventsReady || !!bridge.waitConnection) &&
            watchReady;
          setConfirmed(valid ? { bridge, hostRunId, backendRunId } : null);
          clearTimeout(linkExpiration);
          if (valid)
            linkExpiration = setTimeout(() => {
              if (mounted) {
                setConfirmed(null);
                reportGap();
              }
            }, remaining);
          else if (!state.linkValid || remaining <= 0) reportGap();
        }
      } else {
        setConfirmed(null);
        reportGap();
      }
      if (
        result &&
        (eventsReady || !!bridge.waitConnection) &&
        watchReady &&
        result.view.connection.revision >= requiredRevision &&
        Number.isSafeInteger(result.view.connection.streamGeneration) &&
        result.view.snapshot &&
        Number.isSafeInteger(result.view.snapshot.lastEventSequence) &&
        result.view.snapshot.lastEventSequence >= requiredSequence
      ) {
        const { view, started } = result;
        const remaining = view.validForMs - (performance.now() - started);
        if (
          view.connection.hostRunId !== hostRunId ||
          !Number.isSafeInteger(view.connection.revision) ||
          view.connection.revision < revision
        ) {
          onView?.(null, 0);
        } else {
          if (streamGeneration !== undefined && view.connection.streamGeneration > streamGeneration)
            reportGap();
          streamGeneration = Math.max(streamGeneration ?? 0, view.connection.streamGeneration);
          revision = view.connection.revision;
          const live =
            view.connection.status === 'live' &&
            view.snapshot?.backendRunId === backendRunId &&
            remaining > 0;
          if (live) {
            snapshotSequence = view.snapshot?.lastEventSequence ?? 0;
            markSnapshotSequence(bridge, snapshotSequence);
          }
          if (view.connection.linkValid) gapReported = false;
          onView?.(live ? view : null, live ? started + view.validForMs : 0);
          clearTimeout(expiration);
          if (live)
            expiration = setTimeout(() => {
              if (mounted) {
                onView?.(null, 0);
              }
            }, remaining);
        }
      } else {
        onView?.(null, 0);
      }
    }
    const stopObserving = readLastSession(bridge, (view, started) => {
      if (heartbeatConfirmed && started > minimumObservationStart) applyResult({ view, started });
      else if (
        !heartbeatConfirmed &&
        view.connection.hostRunId === hostRunId &&
        Number.isSafeInteger(view.connection.revision) &&
        Number.isSafeInteger(view.connection.streamGeneration) &&
        view.connection.revision > linkRevision
      ) {
        linkRevision = view.connection.revision;
        linkGeneration = Math.max(linkGeneration, view.connection.streamGeneration);
      }
    });
    function read() {
      if (!eventsReady) ensureEvents();
      unsubscribe = readSession(bridge, (result) => {
        if (!mounted) return;
        if (result) heartbeatConfirmed = true;
        else {
          heartbeatConfirmed = false;
          minimumObservationStart = performance.now();
        }
        applyResult(result);
        timer = setTimeout(read, 2000);
      });
    }
    read();
    return () => {
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
  }, [backendRunId, bridge, hostRunId, onView, onGap]);

  const isConfirmed =
    confirmed?.bridge === bridge &&
    confirmed.hostRunId === hostRunId &&
    confirmed.backendRunId === backendRunId;
  const [showUpdating, setShowUpdating] = useState(false);
  useEffect(() => {
    if (!isConfirmed || snapshotAccepted) {
      setShowUpdating(false);
      return undefined;
    }
    const timer = setTimeout(() => setShowUpdating(true), 500);
    return () => clearTimeout(timer);
  }, [isConfirmed, snapshotAccepted]);
  return (
    <div role="status" className="session-status">
      <p className="session-status-title">
        <span>
          {isConfirmed
            ? 'При последней проверке связь с ядром подтверждена'
            : 'Состояние управления не подтверждено'}
        </span>
      </p>
      <p className="session-status-help">
        <span>
          {isConfirmed
            ? showUpdating
              ? 'Ожидаем актуальное состояние ядра; действия временно недоступны.'
              : ''
            : 'Если связь не восстанавливается, закройте приложение и запустите его снова.'}
        </span>
      </p>
    </div>
  );
}
