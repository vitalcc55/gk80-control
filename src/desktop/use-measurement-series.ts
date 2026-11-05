import { useEffect, useSyncExternalStore } from 'react';
import type { Snapshot } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { MeasurementSeriesContext } from './measurement-series';

export function useMeasurementSeries(
  context: MeasurementSeriesContext,
  key: string,
  snapshot: Snapshot | null,
  fresh: boolean,
  gapVersion: number,
) {
  const series = useSyncExternalStore(context.subscribe, () => context.read(key));
  useEffect(() => {
    context.accept(key, snapshot, fresh, gapVersion);
  }, [context, key, snapshot, fresh, gapVersion]);
  useEffect(() => () => context.markGap(key), [context, key]);
  return series;
}
