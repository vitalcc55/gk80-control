import type { Fixture, FixtureId, GraphPoint, Measurement, StageId, TestParameters } from './model';

/** Сценарии только отображают подготовленные состояния и не выполняют испытание. */
export const stages: readonly { id: StageId; label: string }[] = [
  { id: 'cycles', label: 'Подготовительные циклы' },
  { id: 'pressure', label: 'Выход на рабочее давление' },
  { id: 'stabilization', label: 'Первичная стабилизация' },
  { id: 'valve', label: 'Открытие и закрытие' },
  { id: 'restabilization', label: 'Стабилизация после закрытия' },
  { id: 'measurement', label: 'Измерение' },
  { id: 'release', label: 'Снижение давления' },
];

export const defaultParameters: TestParameters = {
  id: 'ДЕМО-001',
  valve: 'Образец клапана',
  area: 0.012,
  pmax: 10000,
  direction: 'pressure',
  actuator: false,
  voltage: 24,
  openSeconds: 3,
  closeSeconds: 3,
  measurementSeconds: 60,
};

const pressure: Measurement = {
  channel: 'ДД1',
  value: 9950,
  lastValue: null,
  quality: 'valid',
  time: '10:42:18',
  unit: 'Па',
  source: 'подготовленные данные',
};

const differential: Measurement = {
  channel: 'ДД2',
  value: 84,
  lastValue: null,
  quality: 'valid',
  time: '10:42:18',
  unit: 'Па',
  source: 'подготовленные данные',
};

const graph: readonly GraphPoint[] = [
  { seconds: 0, pressure: 9720, differential: 70 },
  { seconds: 5, pressure: 9820, differential: 75 },
  { seconds: 10, pressure: 9890, differential: 78 },
  { seconds: 15, pressure: 9950, differential: 81 },
  { seconds: 20, pressure: 10010, differential: 82 },
  { seconds: 25, pressure: 10040, differential: 84 },
  { seconds: 30, pressure: 10010, differential: 83 },
  { seconds: 35, pressure: 9980, differential: 82 },
  { seconds: 40, pressure: 9960, differential: 84 },
  { seconds: 45, pressure: 9940, differential: 85 },
  { seconds: 50, pressure: 9930, differential: 84 },
  { seconds: 55, pressure: 9940, differential: 85 },
  { seconds: 60, pressure: 9950, differential: 84 },
];

const base: Fixture = {
  id: 'waiting',
  label: 'Ожидание оператора',
  screen: 'run',
  parameters: defaultParameters,
  stage: 'valve',
  pressure,
  differential,
  graph,
  instruction: 'Откройте клапан, затем подтвердите выполненное действие.',
  actionLabel: 'Подтверждаю открытие',
  status: 'Ожидается действие оператора',
  saveStatus: 'Пример: запись ещё не завершена',
};

export const fixtures: Record<FixtureId, Fixture> = {
  empty: {
    ...base,
    id: 'empty',
    label: 'Пустая история',
    screen: 'tests',
  },
  readiness: {
    ...base,
    id: 'readiness',
    label: 'Неполная готовность',
    screen: 'readiness',
  },
  waiting: base,
  measuring: {
    ...base,
    id: 'measuring',
    label: 'Измерение',
    stage: 'measurement',
    instruction: 'Идёт пример измерения. Контролируйте давление и качество данных.',
    actionLabel: 'Показать результат',
    status: 'Измерение · подготовленное состояние',
  },
  'sensor-lost': {
    ...base,
    id: 'sensor-lost',
    label: 'Потеря ДД1',
    stage: 'measurement',
    pressure: { ...pressure, value: null, lastValue: pressure.value, quality: 'stale' },
    graph: graph.map((point) => (point.seconds >= 50 ? { ...point, pressure: null } : point)),
    instruction: 'Данные ДД1 недоступны. Текущее давление неизвестно.',
    actionLabel: 'Показать остановку',
    status: 'Потеря данных ДД1',
  },
  stopping: {
    ...base,
    id: 'stopping',
    label: 'Остановка выполняется',
    stage: 'release',
    instruction: 'Запрос остановки показан как принятый в макете. Результат ещё не подтверждён.',
    actionLabel: 'Показать результат',
    status: 'Остановка выполняется',
  },
  'save-error': {
    ...base,
    id: 'save-error',
    label: 'Ошибка сохранения',
    screen: 'result',
    stage: 'release',
    instruction: 'Пример ошибки сохранения: данные не подтверждены как записанные.',
    status: 'Проход завершён в примере',
    saveStatus: 'Ошибка сохранения · пример',
  },
  actuator: {
    ...base,
    id: 'actuator',
    label: 'Клапан с приводом',
    parameters: { ...defaultParameters, actuator: true },
    stage: 'cycles',
    instruction: 'Подача команды приводу показана в примере; положение клапана не подтверждено.',
    actionLabel: 'Показать следующий этап',
    status: 'Подготовительные циклы · пример',
  },
};

export const fixtureIds: readonly FixtureId[] = [
  'empty',
  'readiness',
  'waiting',
  'measuring',
  'sensor-lost',
  'stopping',
  'save-error',
  'actuator',
];

export function getFixture(id: string | null): Fixture | undefined {
  const match = fixtureIds.find((item) => item === id);
  return match === undefined ? undefined : fixtures[match];
}
