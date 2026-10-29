import { defaultParameters, fixtureIds, fixtures, getFixture, stages } from './fixtures';
import type { Fixture, FixtureId, StageId, TestParameters } from './model';

/** Временная граница представления; при интеграции сопоставляется с контрактом core. */
export interface PreviewSource {
  readonly initial: TestParameters;
  readonly ids: readonly FixtureId[];
  readonly stages: readonly { id: StageId; label: string }[];
  get(id: FixtureId): Fixture;
  find(id: string | null): Fixture | undefined;
}

/** Единственная точка подключения подготовленного источника данных для макета. */
export const fixtureSource: PreviewSource = {
  initial: defaultParameters,
  ids: fixtureIds,
  stages,
  get: (id: FixtureId): Fixture => fixtures[id],
  find: getFixture,
};

/** Точность входных данных сохраняется по умолчанию; для подготовленных значений её можно указать отдельно. */
export function formatNumber(value: number, maximumSignificantDigits = 21): string {
  const magnitude = Math.abs(value);
  const scientific = magnitude >= 1e12 || (magnitude > 0 && magnitude < 1e-9);
  return new Intl.NumberFormat('ru-RU', {
    notation: scientific ? 'scientific' : 'standard',
    maximumSignificantDigits,
  }).format(value);
}

/** Переходы предназначены только для просмотра; это не алгоритм core. */
export const demoSteps: readonly {
  stage: StageId;
  title: string;
  instruction: string;
  action: string;
}[] = [
  {
    stage: 'cycles',
    title: 'Подготовительные циклы',
    instruction: 'Выполните 10 циклов открытия и закрытия. После серии клапан должен быть закрыт.',
    action: 'Подтверждаю завершение серии',
  },
  {
    stage: 'pressure',
    title: 'Выход на рабочее давление',
    instruction: 'Контролируйте выход давления на рабочую точку.',
    action: 'Показать стабилизацию',
  },
  {
    stage: 'stabilization',
    title: 'Первичная стабилизация',
    instruction: 'Удержание давления: ±2% в течение 10 секунд без перерыва.',
    action: 'Показать следующий этап',
  },
  {
    stage: 'valve',
    title: 'Открытие клапана',
    instruction: 'Откройте клапан, затем подтвердите выполненное действие.',
    action: 'Подтверждаю открытие',
  },
  {
    stage: 'valve',
    title: 'Закрытие клапана',
    instruction: 'Закройте клапан, затем подтвердите выполненное действие.',
    action: 'Подтверждаю закрытие',
  },
  {
    stage: 'restabilization',
    title: 'Стабилизация после закрытия',
    instruction: 'Удержание давления: ±5% в течение заданного интервала.',
    action: 'Показать измерение',
  },
  {
    stage: 'measurement',
    title: 'Измерение',
    instruction: 'Контролируйте давление и качество данных.',
    action: 'Показать снижение давления',
  },
  {
    stage: 'release',
    title: 'Снижение давления',
    instruction: 'Снижение давления. Критерий окончания требует согласования.',
    action: 'Показать результат',
  },
];

/** Подгоняет синтетический ряд для обсуждения выбранной точки, не выполняя измерение. */
export function prepareRunDisplay(fixture: Fixture, parameters: TestParameters, stage: StageId) {
  const scale = (value: number): number | null => {
    if (parameters.pmax === defaultParameters.pmax) return value;
    const scaled = (value / defaultParameters.pmax) * parameters.pmax;
    return Number.isFinite(scaled) && (scaled !== 0 || value === 0) ? scaled : null;
  };
  const released = (ratio: number): number | null => {
    const scaled = parameters.pmax * ratio;
    return Number.isFinite(scaled) && (scaled !== 0 || ratio === 0) ? scaled : null;
  };
  const noData = stage === 'cycles';
  const releasing =
    stage === 'release' && fixture.pressure.quality === 'valid' && fixture.pressure.value !== null;
  return {
    graph: fixture.graph
      .filter((point) => stage !== 'measurement' || point.seconds <= 30)
      .map((point) => ({
        ...point,
        seconds:
          stage === 'measurement'
            ? (point.seconds / 60) * parameters.measurementSeconds
            : point.seconds,
        pressure:
          noData || point.pressure === null
            ? null
            : releasing
              ? released(1 - point.seconds / 75)
              : scale(point.pressure),
        differential: noData ? null : point.differential,
      })),
    pressureValue:
      noData || fixture.pressure.quality !== 'valid'
        ? null
        : releasing
          ? released(0.2)
          : fixture.pressure.value === null
            ? null
            : scale(fixture.pressure.value),
    lastPressure: fixture.pressure.lastValue === null ? null : scale(fixture.pressure.lastValue),
    differentialValue: noData ? null : fixture.differential.value,
  };
}
