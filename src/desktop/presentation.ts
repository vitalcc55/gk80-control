import type {
  Snapshot,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { TestParameters } from '../model';

/** A running record alone does not establish that the first sample was accepted. */
export function measurementLabel(timing: Snapshot['measurementTiming'] | undefined): string {
  return timing ? 'Измерение' : 'Ожидание данных измерения';
}

export function isStartMeasurementInstruction(instruction: string | undefined): boolean {
  return instruction === 'Подтвердите начало демонстрационного измерения';
}

/** Fields that can change while a result and its final stored pages are being published. */
export function resultReadVersion(record: TestRecord): string {
  return `${record.testId}:${record.stage}:${record.outcome}:${record.dataQuality}:${record.saveStatus}`;
}

export function parametersForDisplay(record: TestRecord): TestParameters {
  return {
    id: record.testId,
    valve: record.parameters.valve,
    area: record.parameters.areaM2,
    pmax: record.parameters.pmaxPa,
    measurementSeconds: record.parameters.measurementSeconds,
    direction: 'pressure',
    actuator: false,
    voltage: 24,
    openSeconds: 3,
    closeSeconds: 3,
  };
}
