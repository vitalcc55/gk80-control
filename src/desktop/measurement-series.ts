import type { Snapshot } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { GraphPoint } from '../model';

export interface MeasurementSeries {
  points: readonly GraphPoint[];
  clockChanged: boolean;
}
interface RetainedSeries {
  value: MeasurementSeries;
  origin: number | null;
  last: { sequence: number; seconds: number } | null;
  gap: boolean;
  gapVersion: number;
}
const empty: MeasurementSeries = { points: [], clockChanged: false };

// Page-owned memory: current and three recent contexts, 300 points each.
// No I/O; disposal of a view does not dispose of the observed test history.
export class MeasurementSeriesContext {
  private readonly entries = new Map<string, RetainedSeries>();
  private readonly listeners = new Set<() => void>();
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  read = (key: string): MeasurementSeries => this.entries.get(key)?.value ?? empty;
  markGap(key: string) {
    const entry = this.entries.get(key);
    if (entry) entry.gap = true;
  }
  accept(key: string, snapshot: Snapshot | null, fresh: boolean, gapVersion: number) {
    if (!key) return;
    let entry = this.entries.get(key);
    if (!entry) {
      entry = { value: empty, origin: null, last: null, gap: false, gapVersion };
      this.entries.set(key, entry);
      if (this.entries.size > 4) {
        const oldest = this.entries.keys().next().value;
        if (oldest !== undefined) this.entries.delete(oldest);
      }
    } else {
      this.entries.delete(key);
      this.entries.set(key, entry);
    }
    if (entry.gapVersion !== gapVersion) {
      entry.gapVersion = gapVersion;
      entry.gap = true;
    }
    if (!fresh) return;
    const pressure = snapshot?.measurements?.find((item) => item.channel === 'dd1');
    const differential = snapshot?.measurements?.find((item) => item.channel === 'dd2');
    if (
      pressure?.quality !== 'valid' ||
      pressure.value === null ||
      !pressure.measuredAt ||
      !Number.isSafeInteger(pressure.sequence)
    ) {
      entry.gap = true;
      return;
    }
    if (entry.last && pressure.sequence <= entry.last.sequence) return;
    const timestamp = Date.parse(pressure.measuredAt);
    if (!Number.isFinite(timestamp)) return;
    if (entry.origin === null) entry.origin = timestamp;
    let seconds = (timestamp - entry.origin) / 1000;
    const clockChanged = !!entry.last && seconds <= entry.last.seconds;
    if (clockChanged) {
      entry.origin = timestamp;
      seconds = 0;
      entry.last = null;
      entry.gap = false;
    }
    const points = clockChanged ? [] : [...entry.value.points];
    if (entry.gap && entry.last)
      points.push({
        seconds: (entry.last.seconds + seconds) / 2,
        pressure: null,
        differential: null,
      });
    points.push({
      seconds,
      pressure: pressure.value,
      differential:
        differential?.quality === 'valid' && differential.measuredAt === pressure.measuredAt
          ? differential.value
          : null,
    });
    entry.last = { sequence: pressure.sequence, seconds };
    entry.gap = false;
    entry.value = {
      points: points.slice(-300),
      clockChanged: clockChanged || entry.value.clockChanged,
    };
    for (const listener of this.listeners) listener();
  }
}
