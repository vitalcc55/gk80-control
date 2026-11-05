import { useCallback, useEffect, useRef, useState } from 'react';
import type { DesktopStatus } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DesktopBridge, SessionBridge } from './bridge';
import type { DemoService } from './demo-service';
import { LiveApp } from './LiveApp';
import { MeasurementSeriesContext } from './measurement-series';
import { SessionStatus } from './SessionStatus';

interface ViewState {
  status: DesktopStatus | null;
  bridgeError: boolean;
}

function validUUID(value: unknown): boolean {
  return (
    typeof value === 'string' &&
    value !== '00000000-0000-0000-0000-000000000000' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
  );
}

function confirmed(status: DesktopStatus): boolean {
  if (status.status !== 'ready') return status.handshake === null;
  return (
    status.handshake?.demo === true &&
    status.handshake.apiVersion === 'v1' &&
    validUUID(status.handshake.backendRunId) &&
    validUUID(status.handshake.databaseId)
  );
}

const labels = {
  starting: 'Запуск ядра',
  ready: 'При запуске ядро ответило',
  profileInUse: 'Профиль уже открыт',
  unavailable: 'Ядро недоступно',
  exited: 'Связь с ядром потеряна',
  closing: 'Завершение работы',
  '': 'Состояние неизвестно',
} as const;

export function DesktopApp({
  bridge,
  session,
  service,
  pendingStorage,
  retainLiveOnError = false,
  measurementContext: providedMeasurementContext,
}: {
  bridge: DesktopBridge;
  session?: SessionBridge;
  service?: DemoService;
  pendingStorage?: Storage;
  retainLiveOnError?: boolean;
  measurementContext?: MeasurementSeriesContext;
}) {
  const current = useRef<ViewState>({ status: null, bridgeError: false });
  const [localMeasurementContext] = useState(() => new MeasurementSeriesContext());
  const measurementContext = providedMeasurementContext ?? localMeasurementContext;
  const [view, setView] = useState<ViewState>(current.current);
  const [pending, setPending] = useState(false);
  const [verifiedBridge, setVerifiedBridge] = useState<DesktopBridge | null>(null);
  const generation = useRef(0);

  const apply = useCallback((next: DesktopStatus, baseline?: ViewState) => {
    const previous = current.current;
    const old = previous.status;
    if (
      old &&
      (old.hostRunId !== next.hostRunId ||
        next.revision < old.revision ||
        (next.revision === old.revision && baseline !== previous))
    )
      return;
    if (old && confirmed(old) && !confirmed(next)) {
      const updated = { ...previous, bridgeError: true };
      current.current = updated;
      setView(updated);
      return;
    }
    const connectionChanged =
      old?.hostRunId !== next.hostRunId || old?.connectionRevision !== next.connectionRevision;
    const bridgeError =
      !confirmed(next) || (previous.bridgeError && !baseline?.bridgeError && !connectionChanged);
    const updated = { status: next, bridgeError };
    current.current = updated;
    setView(updated);
  }, []);

  const fail = useCallback((baseline: ViewState) => {
    const latest = current.current;
    if (
      latest !== baseline &&
      (latest.status?.hostRunId !== baseline.status?.hostRunId ||
        latest.status?.connectionRevision !== baseline.status?.connectionRevision)
    )
      return;
    const updated = { ...latest, bridgeError: true };
    current.current = updated;
    setView(updated);
  }, []);

  useEffect(() => {
    generation.current += 1;
    let subscribed = true;
    const accept = (next: DesktopStatus, baseline?: ViewState) => {
      apply(next, baseline);
      if (current.current.status === next && confirmed(next)) setVerifiedBridge(bridge);
    };
    const unsubscribe = bridge.subscribe(
      (next, polled) => {
        if (subscribed) accept(next, polled ? current.current : undefined);
      },
      () => {
        if (subscribed && current.current.status?.status !== 'ready') fail(current.current);
      },
    );
    const baseline = current.current;
    void bridge.status().then(
      (next) => {
        if (subscribed) accept(next, baseline);
      },
      () => {
        if (subscribed) fail(baseline);
      },
    );
    return () => {
      subscribed = false;
      generation.current += 1;
      unsubscribe();
    };
  }, [apply, bridge, fail]);

  async function verify() {
    const currentGeneration = generation.current;
    const baseline = current.current;
    setPending(true);
    try {
      const result = await bridge.verify();
      if (generation.current === currentGeneration) {
        apply(result, baseline);
        if (current.current.status === result && confirmed(result)) setVerifiedBridge(bridge);
      }
    } catch {
      if (generation.current === currentGeneration) fail(baseline);
    } finally {
      if (generation.current === currentGeneration) setPending(false);
    }
  }

  const { status } = view;
  const bridgeError = view.bridgeError || (retainLiveOnError && verifiedBridge !== bridge);
  const label = bridgeError
    ? 'Не удалось получить состояние приложения'
    : status
      ? labels[status.status]
      : 'Запуск ядра';
  if (
    (!bridgeError || retainLiveOnError) &&
    status?.status === 'ready' &&
    confirmed(status) &&
    status.handshake &&
    session &&
    service
  ) {
    return (
      <>
        {bridgeError && <p role="alert">Не удалось подтвердить состояние приложения.</p>}
        <div hidden={bridgeError}>
          <LiveApp
            measurementContext={measurementContext}
            session={session}
            service={service}
            hostRunId={status.hostRunId}
            backendRunId={status.handshake.backendRunId}
            databaseId={status.handshake.databaseId}
            diagnosticsDegraded={status.diagnosticsDegraded}
            suspended={bridgeError}
            {...(pendingStorage ? { pendingStorage } : {})}
          />
        </div>
      </>
    );
  }
  return (
    <main className="desktop-shell">
      <header className="desktop-header">
        <strong>ГК80</strong>
        <span>Демонстрация · тестовые данные</span>
      </header>
      <section className="desktop-card" aria-labelledby="desktop-title">
        <p className="desktop-eyebrow">Проверка приложения</p>
        <h1 id="desktop-title">Связь с демонстрационным ядром</h1>
        <p role="status" aria-live="polite" className="desktop-status">
          {label}
        </p>
        {!bridgeError && status?.status === 'ready' && status.handshake ? (
          <>
            <p>Ядро ответило. Версия интерфейса: {status.handshake.apiVersion}.</p>
            {session && (
              <SessionStatus
                bridge={session}
                hostRunId={status.hostRunId}
                backendRunId={status.handshake.backendRunId}
              />
            )}
          </>
        ) : null}
        {status?.status === 'profileInUse' ? (
          <p>Закройте другое окно ГК80 и запустите приложение снова.</p>
        ) : null}
        {status?.status === 'unavailable' || status?.status === 'exited' ? (
          <p>Проверьте приложение и повторите запуск.</p>
        ) : null}
        <p role="status" aria-live="polite" className="desktop-warning">
          {status?.diagnosticsDegraded ? 'Журнал диагностики недоступен или неполон.' : ''}
        </p>
        <p className="desktop-note">
          Испытания и сохранённые записи доступны после подключения ядра.
        </p>
        <button
          type="button"
          disabled={pending || (!bridgeError && status?.status !== 'ready')}
          onClick={() => {
            void verify();
          }}
        >
          {pending ? 'Проверяем…' : bridgeError ? 'Повторить связь' : 'Проверить связь'}
        </button>
      </section>
    </main>
  );
}
