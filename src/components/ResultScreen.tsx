import { ArrowLeft, ArrowRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { outcomeLabel, qualityLabel, saveLabel } from '../desktop/display';
import type { TestRecord } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatNumber } from '../lib/format';
import type { LeakagePoint, TestParameters } from '../model';
import { LeakageChart } from './LeakageChart';
import { PressureNotation } from './PressureNotation';
import './screens.css';

interface ResultScreenProps {
  parameters: TestParameters;
  saveError: boolean;
  interrupted: boolean;
  active: boolean;
  draftAvailable: boolean;
  onList: () => void;
  onNew: () => void;
  stopConfirmation?: { disabled: boolean; onConfirm: () => void } | undefined;
  record?: TestRecord;
  previewPoint?: LeakagePoint | undefined;
  children?: ReactNode;
  hidden?: boolean;
}

const unavailableLeakage = {
  missingTestPressure: 'Нет пригодных показаний ДД1.',
  missingAirConditions: 'Недостаточно данных о параметрах воздуха.',
  noUsableDifferential: 'Нет пригодных показаний перепада на диафрагме.',
  invalidCalculationInputs: 'Непригодные исходные данные расчёта.',
} as const;

export function ResultScreen({
  parameters,
  saveError,
  interrupted,
  active,
  draftAvailable,
  onList,
  onNew,
  stopConfirmation,
  record,
  previewPoint,
  children,
  hidden = false,
}: ResultScreenProps) {
  const savedCalculation = record?.saveStatus === 'saved' ? record.leakageCalculation : undefined;
  const values = savedCalculation?.values;
  const unavailableReason = savedCalculation?.unavailableReason;
  const emptyMessage = unavailableReason
    ? unavailableLeakage[unavailableReason]
    : values
      ? 'Координаты маркера должны быть положительными для логарифмической шкалы.'
      : record?.saveStatus === 'pending'
        ? 'Ожидается сохранение расчёта.'
        : record?.saveStatus === 'failed' || record?.saveStatus === 'unknown'
          ? 'Сохранение расчёта не подтверждено.'
          : 'Расчёт отсутствует.';
  return (
    <section className="workpage result-page" aria-labelledby="result-title" hidden={hidden}>
      {!hidden && (
        <>
          <div className="workpage-heading">
            <div>
              <h1 id="result-title">Результат испытания</h1>
              <p>{record ? parameters.valve : `${parameters.id} · ${parameters.valve}`}</p>
            </div>
          </div>
          <div className="work-surface result-section">
            <LeakageChart
              point={record ? (values ?? undefined) : previewPoint}
              emptyMessage={record ? emptyMessage : 'Рабочая точка недоступна'}
            />
          </div>
          <div className="result-layout">
            <div className="work-surface result-section">
              <div className="section-heading">
                <h2>Параметры прохода</h2>
              </div>
              <dl className="result-details">
                <div>
                  <dt>Изделие</dt>
                  <dd>{parameters.valve}</dd>
                </div>
                <div>
                  <dt>Площадь клапана</dt>
                  <dd>
                    {formatNumber(parameters.area)} м<sup>2</sup>
                  </dd>
                </div>
                <div>
                  <dt>
                    Давление <PressureNotation />
                  </dt>
                  <dd>{formatNumber(parameters.pmax)} Па</dd>
                </div>
                <div>
                  <dt>Направление</dt>
                  <dd>{parameters.direction === 'pressure' ? 'Нагнетание' : 'Разрежение'}</dd>
                </div>
                <div>
                  <dt>Привод</dt>
                  <dd>{parameters.actuator ? 'Есть' : 'Нет'}</dd>
                </div>
                <div>
                  <dt>Длительность измерения</dt>
                  <dd>{formatNumber(parameters.measurementSeconds)} с</dd>
                </div>
                <div>
                  <dt>Стабилизация перед открытием клапана</dt>
                  <dd>{formatNumber(parameters.preOpenStabilizationSeconds)} с</dd>
                </div>
                <div>
                  <dt>Стабилизация после закрытия клапана</dt>
                  <dd>{formatNumber(parameters.postCloseStabilizationSeconds)} с</dd>
                </div>
                <div>
                  <dt>Повторы для каждой стабилизации</dt>
                  <dd>{parameters.postCloseStabilizationRetries ?? 'Не указано'}</dd>
                </div>
                {[
                  ['Нижний аварийный предел', parameters.lowerPressureLimitPa, 'Па'],
                  ['Выдержка ниже нижнего предела', parameters.lowerPressureHoldSeconds, 'с'],
                  ['Верхний аварийный предел', parameters.upperPressureLimitPa, 'Па'],
                  ['Выдержка выше верхнего предела', parameters.upperPressureHoldSeconds, 'с'],
                ].map(([label, value, unit]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>
                      {typeof value === 'number' ? `${formatNumber(value)} ${unit}` : 'Не указано'}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
            {record && (
              <div className="work-surface result-section">
                <div className="section-heading">
                  <h2>Результаты расчёта</h2>
                </div>
                <dl className="result-details">
                  {values ? (
                    <>
                      <div>
                        <dt>Рабочий объёмный расход</dt>
                        <dd>{formatNumber(values.workingFlowM3PerSecond)} м³/с</dd>
                      </div>
                      <div>
                        <dt>Приведённый расход при 20 °C и 101 325 Па</dt>
                        <dd>{formatNumber(values.normalizedFlowM3PerHour)} м³/ч</dd>
                      </div>
                      <div>
                        <dt>Условия расчёта</dt>
                        <dd>Параметры симулятора</dd>
                      </div>
                    </>
                  ) : (
                    <div>
                      <dt>Расход</dt>
                      <dd>{record.saveStatus !== 'saved' ? 'Недоступен' : 'Не рассчитан'}</dd>
                    </div>
                  )}
                </dl>
              </div>
            )}
          </div>
        </>
      )}
      {children}
      {!hidden && (
        <>
          <div className="result-overview">
            <div>
              <span>Ход испытания</span>
              <strong>
                {record ? outcomeLabel(record.outcome) : interrupted ? 'Прервано' : 'Завершено'}
              </strong>
            </div>
            <div>
              <span>Данные</span>
              <strong>
                {record
                  ? qualityLabel(record.dataQuality)
                  : interrupted
                    ? 'Неполные'
                    : 'Данные доступны'}
              </strong>
            </div>
            <div>
              <span>Сохранение</span>
              <strong className={saveError ? 'error-text' : ''}>
                {record
                  ? saveLabel(record.saveStatus)
                  : saveError
                    ? 'Сохранение не подтверждено'
                    : 'Сохранение не выполнялось'}
              </strong>
              {saveError && <p>Запись результата не подтверждена.</p>}
            </div>
          </div>
          <div className="work-actions">
            <button className="work-button work-button-secondary" type="button" onClick={onList}>
              <ArrowLeft aria-hidden="true" size={16} /> К списку
            </button>
            <button
              className="work-button work-button-primary"
              type="button"
              onClick={stopConfirmation?.onConfirm ?? onNew}
              disabled={active || stopConfirmation?.disabled}
            >
              {stopConfirmation ? (
                'Подтвердить остановку вала'
              ) : (
                <>
                  {draftAvailable && !active ? 'Продолжить подготовку' : 'Новое по этим параметрам'}{' '}
                  <ArrowRight aria-hidden="true" size={16} />
                </>
              )}
            </button>
          </div>
        </>
      )}
    </section>
  );
}
