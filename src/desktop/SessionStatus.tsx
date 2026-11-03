import { useEffect, useState } from 'react';
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
              ? showUpdating
                ? 'Ожидаем актуальное состояние ядра; действия временно недоступны.'
                : ''
              : 'Состояние испытания не подтверждено; последние показания могут быть неактуальны. Если связь не восстанавливается, перезапустите приложение.'}
        </span>
      </p>
    </div>
  );
}
