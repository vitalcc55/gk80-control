import type { ReactNode } from 'react';
import { RunScreen } from '../components/RunScreen';
import type {
  Measurement,
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import {
  formatElapsedSeconds,
  formatMeasurement,
  formatNumber,
  formatRemainingSeconds,
  unavailableValue,
} from '../lib/format';
import { measurementQualityLabel } from './display';
import { measurementLabel } from './presentation';

function shown(item: Measurement | undefined): string {
  return item?.quality === 'valid' || item?.quality === 'stale'
    ? formatMeasurement(item.value)
    : unavailableValue;
}
export function LiveRun({
  record,
  snapshot,
  fresh,
  pending,
  onConfirm,
  stop,
}: {
  record: TestRecord;
  snapshot: Snapshot;
  fresh: boolean;
  pending: boolean;
  onConfirm: () => void;
  stop?: ReactNode;
}) {
  const readings = snapshot.currentReadings?.length
    ? snapshot.currentReadings
    : snapshot.measurements;
  const pressure = readings?.find((item) => item.channel === 'dd1');
  const differential = readings?.find(
    (item) => item.channel === (snapshot.activeDifferential ?? 'dd2'),
  );
  const stopping = record.stage === 'stopping';
  const awaiting = !!snapshot.pendingAction;
  const timing = snapshot.phaseTiming ?? snapshot.measurementTiming;
  const phases = [
    ['awaitingPreparation', 'Подготовительные циклы'],
    ['searchingPressure', 'Набор давления'],
    ['stabilizingBeforeOpening', 'Стабилизация перед открытием'],
    ['awaitingValveOpening', 'Открытие клапана'],
    ['awaitingValveClosing', 'Закрытие клапана'],
    ['stabilizingAfterClosing', 'Стабилизация после закрытия'],
    ['measuring', 'Измерение'],
    ['stopping', 'Остановка'],
  ] as const;
  const phase = phases.findIndex(([id]) => id === (stopping ? 'stopping' : snapshot.runPhase));
  const instruction = stopping
    ? 'По последнему снимку: этап остановки. Безопасное давление не подтверждено.'
    : snapshot.pendingAction?.instruction;
  const confirmationLabel = {
    confirmPreparation: 'Подтвердить подготовку',
    confirmValveOpen: 'Подтвердить открытие клапана',
    confirmValveClosed: 'Подтвердить закрытие клапана',
    confirmStopped: 'Подтвердить остановку вала',
  } as const;
  return (
    <RunScreen
      subtitle={<>{record.parameters.valve} · Нагнетание · Без привода</>}
      stages={phases.map(([id, label], index) => ({
        id,
        label,
        state:
          index === phase ? 'current' : stopping ? 'inactive' : index < phase ? 'done' : 'next',
      }))}
      pressure={shown(pressure)}
      differential={shown(differential)}
      differentialChannel={snapshot.activeDifferential === 'dd3' ? 'ДД3' : 'ДД2'}
      target={`${formatNumber(record.parameters.pmaxPa)} Па`}
      pressureNote={
        pressure &&
        pressure.quality !== 'valid' && <p>{measurementQualityLabel(pressure.quality)}</p>
      }
      differentialNote={
        differential &&
        differential.quality !== 'valid' && <p>{measurementQualityLabel(differential.quality)}</p>
      }
      leakageMessage={stopping ? 'Рабочая точка недоступна' : 'Рабочая точка ещё не получена'}
      actionTitle={phases[phase]?.[1] ?? measurementLabel(timing)}
      actionLabel={
        stopping
          ? 'Состояние по последнему полученному снимку'
          : 'Текущее действие по последнему полученному снимку'
      }
      instruction={instruction}
      time={
        <div className="run-timing">
          <dl className="run-time">
            <div>
              <dt>Прошло</dt>
              <dd>
                {timing ? `${formatElapsedSeconds(timing.elapsedSeconds)} с` : unavailableValue}
              </dd>
            </div>
            <div>
              <dt>Осталось</dt>
              <dd>
                {timing ? `${formatRemainingSeconds(timing.remainingSeconds)} с` : unavailableValue}
              </dd>
            </div>
          </dl>
        </div>
      }
      action={
        awaiting &&
        !stopping && (
          <button
            className="primary-button full-width"
            type="button"
            disabled={!fresh || pending}
            onClick={onConfirm}
          >
            {snapshot.pendingAction?.kind
              ? confirmationLabel[snapshot.pendingAction.kind]
              : 'Подтвердить действие'}
          </button>
        )
      }
      stop={stop}
    />
  );
}
