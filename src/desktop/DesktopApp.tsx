import { useCallback, useEffect, useRef, useState } from 'react';
import type { DesktopStatus } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DesktopBridge, SessionBridge } from './bridge';
import { LiveApp } from './LiveApp';
import { SessionStatus } from './SessionStatus';

interface ViewState {
  status: DesktopStatus | null;
  bridgeError: boolean;
}

function confirmed(status: DesktopStatus): boolean {
  if (status.status !== 'ready') return status.handshake === null;
  return (
    status.handshake?.demo === true &&
    status.handshake.apiVersion === 'v1' &&
    status.handshake.backendRunId !== '00000000-0000-0000-0000-000000000000' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      status.handshake.backendRunId,
    )
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
}: {
  bridge: DesktopBridge;
  session?: SessionBridge;
}) {
  const current = useRef<ViewState>({ status: null, bridgeError: false });
  const [view, setView] = useState<ViewState>(current.current);
  const [pending, setPending] = useState(false);
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
    const unsubscribe = bridge.subscribe((next) => {
      if (subscribed) apply(next);
    });
    const baseline = current.current;
    void bridge.status().then(
      (next) => {
        if (subscribed) apply(next, baseline);
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
      if (generation.current === currentGeneration) apply(result, baseline);
    } catch {
      if (generation.current === currentGeneration) fail(baseline);
    } finally {
      if (generation.current === currentGeneration) setPending(false);
    }
  }

  const { status, bridgeError } = view;
  const label = bridgeError
    ? 'Не удалось получить состояние приложения'
    : status
      ? labels[status.status]
      : 'Запуск ядра';
  if (!bridgeError && status?.status === 'ready' && status.handshake && session) {
    return (
      <LiveApp
        session={session}
        hostRunId={status.hostRunId}
        backendRunId={status.handshake.backendRunId}
        diagnosticsDegraded={status.diagnosticsDegraded}
      />
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
