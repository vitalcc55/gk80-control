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
  const factor = parameters.pmax / defaultParameters.pmax;
  const noData = stage === 'cycles';
  const releasing =
    stage === 'release' && fixture.pressure.quality === 'valid' && fixture.pressure.value !== null;
  return {
    graph: fixture.graph.map((point) => ({
      ...point,
      pressure:
        noData || point.pressure === null
          ? null
          : releasing
            ? parameters.pmax * (1 - point.seconds / 75)
            : point.pressure * factor,
      differential: noData ? null : point.differential,
    })),
    pressureValue:
      noData || fixture.pressure.quality !== 'valid'
        ? null
        : releasing
          ? parameters.pmax * 0.2
          : fixture.pressure.value === null
            ? null
            : fixture.pressure.value * factor,
    lastPressure: fixture.pressure.lastValue === null ? null : fixture.pressure.lastValue * factor,
    differentialValue: noData ? null : fixture.differential.value,
  };
}
