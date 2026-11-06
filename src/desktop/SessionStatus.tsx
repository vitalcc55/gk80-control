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
  const [notice, setNotice] = useState<'none' | 'unconfirmed' | 'restored'>('none');
  useEffect(() => {
    if (confirmedSnapshot) {
      setNotice((previous) => (previous === 'unconfirmed' ? 'restored' : 'none'));
      return;
    }
    setNotice('none');
    const timer = window.setTimeout(() => {
      setNotice('unconfirmed');
    }, 1500);
    return () => window.clearTimeout(timer);
  }, [confirmedSnapshot]);
  return (
    <div className="session-status">
      <div role="status" aria-atomic="true">
        <p className="session-status-title">
          <span>
            {needsRestart
              ? 'Управление не подтверждено'
              : isConfirmed
                ? 'Связь подтверждена'
                : 'Связь не подтверждена'}
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
      <SnapshotStatus unconfirmed={!confirmedSnapshot && notice === 'unconfirmed'} />
      <span className="sr-only" role="status">
        {notice === 'unconfirmed'
          ? 'Состояние не подтверждено. Обычные команды временно недоступны.'
          : notice === 'restored'
            ? 'Состояние обновлено'
            : ''}
      </span>
    </div>
  );
}
