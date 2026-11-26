import { describe, expect, it } from 'vitest';
import { prepareRunDisplay } from '../src/fixtureAdapter';
import { fixtureIds, getFixture } from '../src/fixtures';
import { formatNumber } from '../src/lib/format';

describe('подготовленные состояния интерфейса', () => {
  it('не показывает принятое малое положительное значение как ноль', () => {
    expect(formatNumber(0.0001)).toBe('1E-4');
    expect(formatNumber(0.00000001)).toBe('1E-8');
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
    expect(formatNumber(derivedPressure)).toBe('1E-100');
    expect(formatNumber(1.2345678901234567)).toBe('1,23');
    expect(formatNumber(0.0001)).toBe('1E-4');
    expect(formatNumber(Number.MIN_VALUE)).toBe('5E-324');
  });

  it('не выдаёт переполнение и потерю точности давления за достоверное показание', () => {
    const scenario = getFixture('measuring');
    if (!scenario) throw new Error('Missing measurement fixture');
    const extreme = { ...scenario.parameters, pmax: Number.MAX_VALUE };
    const measurement = prepareRunDisplay(scenario, extreme, 'measurement');
    expect(measurement.pressureValue).toBeNull();
    const release = prepareRunDisplay(
      scenario,
      { ...scenario.parameters, pmax: Number.MIN_VALUE },
      'release',
    );
    expect(release.pressureValue).toBeNull();
  });

  it('открывает все документированные состояния по постоянным идентификаторам', () => {
    expect(new Set(fixtureIds).size).toBe(fixtureIds.length);
    for (const id of fixtureIds) {
      expect(getFixture(id)?.id).toBe(id);
    }
    expect(getFixture('unknown')).toBeUndefined();
    expect(getFixture(null)).toBeUndefined();
  });

  it('не подменяет потерю ДД1 нулём', () => {
    const scenario = getFixture('sensor-lost');
    expect(scenario?.pressure.channel).toBe('ДД1');
    expect(scenario?.pressure.value).toBeNull();
    expect(scenario?.pressure.quality).toBe('stale');
    expect(scenario?.pressure.lastValue).not.toBeNull();
  });

  it('показывает последнее доступное показание при потере ДД1 во время снижения', () => {
    const scenario = getFixture('sensor-lost');
    if (!scenario) throw new Error('Missing sensor-loss fixture');
    const display = prepareRunDisplay(scenario, scenario.parameters, 'release');
    expect(display.pressureValue).toBeNull();
    expect(display.lastPressure).toBe(scenario.pressure.lastValue);
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
});
