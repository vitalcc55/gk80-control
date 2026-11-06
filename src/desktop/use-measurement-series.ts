import { useEffect, useSyncExternalStore } from 'react';
import type { Snapshot } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { MeasurementSeriesContext } from './measurement-series';

/** Collection remains mounted with the accepted session projection, including hidden screens. */
export function useMeasurementObservation(
  context: MeasurementSeriesContext,
  key: string,
  snapshot: Snapshot | null,
  fresh: boolean,
  gapVersion: number,
) {
  useEffect(() => {
    context.accept(key, snapshot, fresh, gapVersion);
  }, [context, key, snapshot, fresh, gapVersion]);
  useEffect(() => () => context.markGap(key), [context, key]);
}

/** Only a mounted graph subscribes to changes of its bounded presentation series. */
export function useMeasurementSeries(context: MeasurementSeriesContext, key: string) {
  return useSyncExternalStore(context.subscribe, () => context.read(key));
}
