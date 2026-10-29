/** Временные типы представления для макета. Они не являются контрактом core. */
export type Screen = 'tests' | 'parameters' | 'readiness' | 'run' | 'result';

export interface TestParameters {
  id: string;
  valve: string;
  area: number;
  pmax: number;
  direction: 'pressure' | 'vacuum';
  actuator: boolean;
  voltage: 24 | 220;
  openSeconds: number;
  closeSeconds: number;
  measurementSeconds: number;
}

export type FixtureId =
  | 'empty'
  | 'readiness'
  | 'readiness-failed'
  | 'waiting'
  | 'measuring'
  | 'sensor-lost'
  | 'stopping'
  | 'save-error'
  | 'actuator';

export type StageId =
  | 'cycles'
  | 'pressure'
  | 'stabilization'
  | 'valve'
  | 'restabilization'
  | 'measurement'
  | 'release';

export interface Measurement {
  channel: 'ДД1' | 'ДД2' | 'ДД3';
  value: number | null;
  lastValue: number | null;
  quality: 'valid' | 'stale' | 'unavailable';
  time: string;
  unit: 'Па';
  source: 'подготовленные данные';
}

/** Секунды от начала примера; null означает разрыв данных. */
export interface GraphPoint {
  seconds: number;
  pressure: number | null;
  differential: number | null;
}

export interface Fixture {
  id: FixtureId;
  label: string;
  screen: Screen;
  parameters: TestParameters;
  stage: StageId;
  pressure: Measurement;
  differential: Measurement;
  graph: readonly GraphPoint[];
  instruction: string;
  actionLabel: string;
  status: string;
  saveStatus: string;
}
