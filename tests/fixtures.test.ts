import { describe, expect, it } from 'vitest';
import { prepareRunDisplay } from '../src/fixtureAdapter';
import { fixtureIds, getFixture } from '../src/fixtures';

describe('подготовленные состояния интерфейса', () => {
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
