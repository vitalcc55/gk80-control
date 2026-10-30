import { useEffect, useState } from 'react';
import { readSession, type SessionBridge } from './bridge';

export function SessionStatus({
  bridge,
  hostRunId,
  backendRunId,
}: {
  bridge: SessionBridge;
  hostRunId: string;
  backendRunId: string;
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
    let unsubscribe: (() => void) | undefined;
    let revision = -1;
    setConfirmed(null);
    function read() {
      unsubscribe = readSession(bridge, (result) => {
        if (!mounted) return;
        if (result) {
          const { view, started } = result;
          const remaining = view.validForMs - (performance.now() - started);
          if (
            view.connection.hostRunId !== hostRunId ||
            !Number.isSafeInteger(view.connection.revision) ||
            view.connection.revision < revision
          ) {
            setConfirmed(null);
          } else {
            revision = view.connection.revision;
            const live =
              view.connection.status === 'live' &&
              view.snapshot?.backendRunId === backendRunId &&
              remaining > 0;
            setConfirmed(live ? { bridge, hostRunId, backendRunId } : null);
            clearTimeout(expiration);
            if (live)
              expiration = setTimeout(() => {
                if (mounted) setConfirmed(null);
              }, remaining);
          }
        } else setConfirmed(null);
        timer = setTimeout(read, 2000);
      });
    }
    read();
    return () => {
      mounted = false;
      clearTimeout(timer);
      clearTimeout(expiration);
      unsubscribe?.();
    };
  }, [backendRunId, bridge, hostRunId]);

  const isConfirmed =
    confirmed?.bridge === bridge &&
    confirmed.hostRunId === hostRunId &&
    confirmed.backendRunId === backendRunId;
  return (
    <div role="status">
      <p>
        {isConfirmed
          ? 'При последней проверке связь с ядром подтверждена'
          : 'Состояние управления не подтверждено'}
      </p>
      {!isConfirmed && (
        <p>Если связь не восстанавливается, закройте приложение и запустите его снова.</p>
      )}
    </div>
  );
}
