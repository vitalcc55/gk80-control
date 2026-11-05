import { useEffect, useState } from 'react';
import { SnapshotStatus } from '../components/SnapshotStatus';
import type { SessionView } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { SessionBridge } from './bridge';
import { useSessionObservation } from './use-session-observation';

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
  const { projection } = useSessionObservation(bridge, hostRunId, backendRunId, onView, onGap);
  const isConfirmed = projection.linkValid;
  const needsRestart = projection.unknownAcquire;
  const confirmedSnapshot = snapshotAccepted && projection.connection !== null;
  const [notice, setNotice] = useState('');
  useEffect(() => {
    if (confirmedSnapshot) {
      setNotice((previous) =>
        previous === 'Снимок не подтверждён; обычные команды временно недоступны.'
          ? 'Снимок подтверждён'
          : '',
      );
      return;
    }
    setNotice('');
    const timer = window.setTimeout(() => {
      setNotice('Снимок не подтверждён; обычные команды временно недоступны.');
    }, 1500);
    return () => window.clearTimeout(timer);
  }, [confirmedSnapshot]);
  return (
    <div className="session-status">
      <div role="status" aria-atomic="true">
        <p className="session-status-title">
          <span>
            {needsRestart
              ? 'Получение управления не подтверждено'
              : isConfirmed
                ? 'При последней проверке связь с ядром подтверждена'
                : 'Состояние управления не подтверждено'}
          </span>
        </p>
        <p className="session-status-help">
          <span>
            {needsRestart
              ? 'Состояние испытания не подтверждено. Закройте приложение. Запустите снова только после завершения ядра.'
              : isConfirmed
                ? ''
                : 'Состояние испытания не подтверждено; последние показания могут быть неактуальны. Если связь не восстанавливается, перезапустите приложение.'}
          </span>
        </p>
      </div>
      <SnapshotStatus
        unconfirmed={
          !confirmedSnapshot &&
          notice === 'Снимок не подтверждён; обычные команды временно недоступны.'
        }
      />
      <span className="sr-only" role="status">
        {notice}
      </span>
    </div>
  );
}
