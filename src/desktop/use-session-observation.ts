import { useEffect, useMemo, useState } from 'react';
import type { SessionView } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { SessionBridge } from './bridge';
import { sessionObservation } from './session-observation';

export function useSessionObservation(
  bridge: SessionBridge,
  hostRunId: string,
  backendRunId: string,
  onView?: (view: SessionView | null, expiresAt: number) => void,
  onGap?: () => void,
) {
  const observer = useMemo(
    () => sessionObservation(hostRunId, backendRunId),
    [hostRunId, backendRunId],
  );
  const [, setRevision] = useState(0);
  useEffect(() => {
    let lastConnection = observer.getSnapshot().connection;
    let lastGap = observer.getSnapshot().gapVersion;
    return observer.subscribe(bridge, (value) => {
      if (value.gapVersion > lastGap) onGap?.();
      lastGap = value.gapVersion;
      if (value.connection !== lastConnection)
        onView?.(value.connection?.view ?? null, value.connection?.expiresAt ?? 0);
      lastConnection = value.connection;
      setRevision((current) => current + 1);
    });
  }, [observer, bridge, onView, onGap]);
  return { observer, projection: observer.getSnapshotFor(bridge) };
}
