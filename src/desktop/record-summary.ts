import type { Measurement } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { GraphPoint } from '../model';
import type { DemoService } from './demo-service';

// Limits prevent a damaged or unexpectedly large history from retaining unbounded data.
const maxMeasurements = 10000;
const maxAuditPages = 100;
const maxSegments = 16;
const pageSize = 500;

export class RecordSummaryLimitError extends Error {}

function timestamp(value: string): bigint {
  const milliseconds = Date.parse(value);
  const fraction = value.match(/\.(\d+)(?:[Zz]|[+-]\d\d:\d\d)$/)?.[1] ?? '';
  if (!Number.isFinite(milliseconds) || fraction.length > 9) throw new Error('Invalid timestamp');
  return BigInt(milliseconds) * 1000000n + BigInt(fraction.padEnd(9, '0').slice(3) || '0');
}

interface MeasurementSegment {
  startedAt: string;
  points: GraphPoint[];
}

export function measurementSegments(items: readonly Measurement[]): MeasurementSegment[] {
  const segments: MeasurementSegment[] = [];
  let points: GraphPoint[] = [];
  let startedAt = '';
  let origin: bigint | undefined;
  let previous: bigint | undefined;
  let current:
    | {
        time: bigint;
        pressure: number | null;
        differential: number | null;
        dd1: boolean;
        dd2: boolean;
      }
    | undefined;
  const flush = () => {
    if (!current || origin === undefined) return;
    points.push({
      seconds: Number(current.time - origin) / 1e9,
      pressure: current.pressure,
      differential: current.differential,
    });
  };
  for (const item of items) {
    if (item.channel !== 'dd1' && item.channel !== 'dd2') continue;
    const when = item.quality === 'valid' && item.measuredAt ? item.measuredAt : item.observedAt;
    const time = timestamp(when);
    const repeatedChannel =
      current?.time === time && (item.channel === 'dd1' ? current.dd1 : current.dd2);
    if ((previous !== undefined && time < previous) || repeatedChannel) {
      flush();
      if (points.length) segments.push({ startedAt, points });
      if (segments.length >= maxSegments)
        throw new RecordSummaryLimitError(
          'Слишком много участков графика. Доступны страницы измерений ниже.',
        );
      points = [];
      origin = undefined;
      current = undefined;
    }
    if (!current || current.time !== time) {
      flush();
      if (origin === undefined) {
        origin = time;
        startedAt = when;
      }
      current = { time, pressure: null, differential: null, dd1: false, dd2: false };
    }
    const value = item.quality === 'valid' ? item.value : null;
    if (item.channel === 'dd1') {
      current.dd1 = true;
      current.pressure = value;
    } else {
      current.dd2 = true;
      current.differential = value;
    }
    previous = time;
  }
  flush();
  if (points.length) segments.push({ startedAt, points });
  return segments;
}

export async function readMeasurementTrace(
  service: DemoService,
  id: string,
  current: () => boolean,
): Promise<MeasurementSegment[]> {
  const items: Measurement[] = [];
  let after = 0;
  for (;;) {
    const page = await service.measurements(id, after, pageSize);
    if (!current()) throw new Error('Read superseded');
    items.push(...(page.items ?? []));
    if (items.length > maxMeasurements)
      throw new RecordSummaryLimitError(
        'График слишком большой для отображения. Доступны страницы измерений ниже.',
      );
    if (page.nextAfter === null) return measurementSegments(items);
    if (!Number.isSafeInteger(page.nextAfter) || page.nextAfter <= after)
      throw new Error('Invalid measurement cursor');
    after = page.nextAfter;
  }
}

export async function readStopReason(
  service: DemoService,
  id: string,
  commandId: string,
  current: () => boolean,
): Promise<string | null> {
  let after = 0;
  for (let pages = 0; pages < maxAuditPages; pages++) {
    const page = await service.audit(id, after, pageSize);
    if (!current()) throw new Error('Read superseded');
    const event = page.items?.find(
      (item) =>
        item.action === 'stop' &&
        item.outcome === 'accepted' &&
        item.commandId === commandId &&
        item.reason,
    );
    if (event?.reason) return event.reason;
    if (page.nextAfter === null) return null;
    if (!Number.isSafeInteger(page.nextAfter) || page.nextAfter <= after)
      throw new Error('Invalid audit cursor');
    after = page.nextAfter;
  }
  throw new RecordSummaryLimitError(
    'Причина не найдена в пределах поиска. Проверьте страницы журнала событий.',
  );
}
