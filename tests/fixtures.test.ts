import { describe, expect, it } from 'vitest';
import { formatNumber, prepareRunDisplay } from '../src/fixtureAdapter';
import { fixtureIds, getFixture } from '../src/fixtures';

describe('подготовленные состояния интерфейса', () => {
  it('не показывает принятое малое положительное значение как ноль', () => {
    expect(formatNumber(0.0001)).toBe('0,0001');
    expect(formatNumber(0.00000001)).toBe('0,00000001');
  });

  it('показывает крайние порядки компактно и сохраняет знак и ненулевую величину', () => {
    expect(formatNumber(1e307)).toBe('1E307');
    expect(formatNumber(1e-10)).toBe('1E-10');
    expect(formatNumber(Number.MIN_VALUE)).toBe('5E-324');
    expect(formatNumber(-1e12)).toBe('-1E12');
    expect(formatNumber(0)).toBe('0');
    expect(formatNumber(Number.MAX_VALUE).length).toBeLessThan(32);
  });

  it('ограничивает отображаемые знаки подготовленного показания, не теряя точность ввода', () => {
    const derivedPressure = (10010 / 10000) * 1e-100;
    expect(formatNumber(derivedPressure, 12)).toBe('1,001E-100');
    expect(formatNumber(1.2345678901234567)).toBe('1,2345678901234567');
    expect(formatNumber(0.0001, 12)).toBe('0,0001');
    expect(formatNumber(Number.MIN_VALUE, 12)).toBe('5E-324');
  });

  it('согласует середину измерения с последней точкой графика', () => {
    const scenario = getFixture('measuring');
    if (!scenario) throw new Error('Missing measurement fixture');
    const display = prepareRunDisplay(scenario, scenario.parameters, 'measurement');
    expect(display.graph.at(-1)?.seconds).toBe(30);
    expect(display.graph.at(-1)?.pressure).toBe(display.pressureValue);
    expect(display.graph.at(-1)?.differential).toBe(display.differentialValue);
    const longer = prepareRunDisplay(
      scenario,
      { ...scenario.parameters, measurementSeconds: 120 },
      'measurement',
    );
    expect(longer.graph.at(-1)?.seconds).toBe(60);
  });

  it('сохраняет конечное время графика для больших допустимых периодов', () => {
    const scenario = getFixture('measuring');
    if (!scenario) throw new Error('Missing measurement fixture');
    for (const measurementSeconds of [1e307, Number.MAX_VALUE]) {
      const display = prepareRunDisplay(
        scenario,
        { ...scenario.parameters, measurementSeconds },
        'measurement',
      );
      expect(display.graph.every((point) => Number.isFinite(point.seconds))).toBe(true);
      expect(display.graph.at(-1)?.seconds).toBe(measurementSeconds / 2);
      expect(
        display.graph.every((point) => point.pressure === null || Number.isFinite(point.pressure)),
      ).toBe(true);
    }
  });

  it('не выдаёт переполнение и потерю точности давления за достоверное показание', () => {
    const scenario = getFixture('measuring');
    if (!scenario) throw new Error('Missing measurement fixture');
    const extreme = { ...scenario.parameters, pmax: Number.MAX_VALUE };
    const measurement = prepareRunDisplay(scenario, extreme, 'measurement');
    expect(measurement.pressureValue).toBeNull();
    expect(
      measurement.graph.every(
        (point) => point.pressure === null || Number.isFinite(point.pressure),
      ),
    ).toBe(true);
    const release = prepareRunDisplay(
      scenario,
      { ...scenario.parameters, pmax: Number.MIN_VALUE },
      'release',
    );
    expect(release.pressureValue).toBeNull();
    expect(
      release.graph.every(
        (point) =>
          point.pressure === null || (Number.isFinite(point.pressure) && point.pressure > 0),
      ),
    ).toBe(true);
  });

  it('согласует устаревшее показание с последним доступным отсчётом', () => {
    const scenario = getFixture('sensor-lost');
    if (!scenario) throw new Error('Missing sensor-loss fixture');
    const display = prepareRunDisplay(scenario, scenario.parameters, 'measurement');
    expect(display.graph.at(-1)?.pressure).toBeNull();
    expect(display.graph.filter((point) => point.pressure !== null).at(-1)?.pressure).toBe(
      display.lastPressure,
    );
  });
  it('открывает все документированные состояния по постоянным идентификаторам', () => {
    expect(new Set(fixtureIds).size).toBe(fixtureIds.length);
    for (const id of fixtureIds) {
      expect(getFixture(id)?.id).toBe(id);
    }
    expect(getFixture('unknown')).toBeUndefined();
    expect(getFixture(null)).toBeUndefined();
  });

  it('не подменяет потерю ДД1 нулём и оставляет разрыв в графике', () => {
    const scenario = getFixture('sensor-lost');
    expect(scenario?.pressure.channel).toBe('ДД1');
    expect(scenario?.pressure.value).toBeNull();
    expect(scenario?.pressure.quality).toBe('stale');
    expect(scenario?.pressure.lastValue).not.toBeNull();
    expect(scenario?.graph.some((point) => point.pressure === null)).toBe(true);
    expect(scenario?.graph.at(-1)?.pressure).toBeNull();
  });

  it('не создаёт текущее давление или измерения на месте разрывов при потере ДД1 во время снижения', () => {
    const scenario = getFixture('sensor-lost');
    if (!scenario) throw new Error('Missing sensor-loss fixture');
    const display = prepareRunDisplay(scenario, scenario.parameters, 'release');
    expect(display.pressureValue).toBeNull();
    expect(display.lastPressure).toBe(scenario.pressure.lastValue);
    expect(display.graph.map((point) => point.pressure)).toEqual(
      scenario.graph.map((point) => point.pressure),
    );
    expect(display.graph.at(-1)?.pressure).toBeNull();
  });

  it('не создаёт показание снижения, если значение отсутствует при допустимом качестве', () => {
    const scenario = getFixture('measuring');
    if (!scenario) throw new Error('Missing measurement fixture');
    const missing = { ...scenario, pressure: { ...scenario.pressure, value: null } };
    expect(prepareRunDisplay(missing, missing.parameters, 'release').pressureValue).toBeNull();
  });

  it('различает давление и перепад и явно обозначает происхождение показаний', () => {
    for (const id of fixtureIds) {
      const scenario = getFixture(id);
      expect(scenario?.pressure.channel).toBe('ДД1');
      expect(['ДД2', 'ДД3']).toContain(scenario?.differential.channel);
      expect(scenario?.pressure.unit).toBe('Па');
      expect(scenario?.differential.unit).toBe('Па');
      expect(scenario?.pressure.source).toBe('подготовленные данные');
      expect(scenario?.differential.source).toBe('подготовленные данные');
    }
  });

  it('различает пример с приводом и ручной вариант без утверждения о положении', () => {
    const manual = getFixture('waiting');
    const actuator = getFixture('actuator');
    expect(manual?.parameters.actuator).toBe(false);
    expect(actuator?.parameters.actuator).toBe(true);
    expect(actuator?.instruction).toMatch(/положение клапана не подтверждено/i);
  });

  it('сохраняет последовательность времени в подготовленных графиках', () => {
    for (const id of fixtureIds) {
      const points = getFixture(id)?.graph ?? [];
      expect(points.length).toBeGreaterThan(1);
      for (let index = 1; index < points.length; index += 1) {
        const previous = points[index - 1];
        const current = points[index];
        expect(previous).toBeDefined();
        expect(current).toBeDefined();
        if (previous && current) {
          expect(current.seconds).toBeGreaterThan(previous.seconds);
        }
      }
    }
  });
});
