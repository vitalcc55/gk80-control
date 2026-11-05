import { ArrowLeft, ArrowRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { outcomeLabel, qualityLabel, saveLabel } from '../desktop/display';
import type { TestRecord } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatNumber } from '../lib/format';
import type { TestParameters } from '../model';
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
  record?: TestRecord;
  children?: ReactNode;
}

export function ResultScreen({
  parameters,
  saveError,
  interrupted,
  active,
  draftAvailable,
  onList,
  onNew,
  record,
  children,
}: ResultScreenProps) {
  return (
    <section className="workpage" aria-labelledby="result-title">
      <div className="workpage-heading">
        <div>
          <h1 id="result-title">Результат испытания</h1>
          <p>
            {parameters.id} · {parameters.valve}
          </p>
        </div>
      </div>
      <div className="result-overview">
        <div>
          <span>Ход испытания</span>
          <strong>
            {record
              ? outcomeLabel(record.outcome)
              : interrupted
                ? 'Прерван в примере'
                : 'Завершён в примере'}
          </strong>
        </div>
        <div>
          <span>Данные</span>
          <strong>
            {record
              ? qualityLabel(record.dataQuality)
              : interrupted
                ? 'Пример: частичные данные'
                : 'Пример: данные доступны'}
          </strong>
        </div>
        <div>
          <span>Сохранение</span>
          <strong className={saveError ? 'error-text' : ''}>
            {record
              ? saveLabel(record.saveStatus)
              : saveError
                ? 'Ошибка сохранения · пример'
                : 'Пример: сохранено'}
          </strong>
          {saveError && <p>Запись результата не подтверждена.</p>}
        </div>
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
              <dt>
                {record ? 'Интервал демонстрационного измерения' : 'Период усреднения утечки'}
              </dt>
              <dd>{formatNumber(parameters.measurementSeconds)} с</dd>
            </div>
          </dl>
        </div>
        <div className="work-surface result-section">
          <div className="section-heading">
            <h2>Результаты расчёта</h2>
            <p>
              {record
                ? 'В демонстрационном проходе не рассчитываются'
                : 'В макете не рассчитываются'}
            </p>
          </div>
          <dl className="result-details">
            <div>
              <dt>Расход</dt>
              <dd>Не рассчитан</dd>
            </div>
            <div>
              <dt>Класс герметичности</dt>
              <dd>Не определён</dd>
            </div>
          </dl>
        </div>
      </div>
      {children}
      <div className="work-actions">
        <button className="work-button work-button-secondary" type="button" onClick={onList}>
          <ArrowLeft aria-hidden="true" size={16} /> К списку
        </button>
        <button
          className="work-button work-button-primary"
          type="button"
          onClick={onNew}
          disabled={active}
          title={active ? 'Вернитесь к активному проходу' : undefined}
        >
          {draftAvailable && !active ? 'Продолжить подготовку' : 'Новое по этим параметрам'}{' '}
          <ArrowRight aria-hidden="true" size={16} />
        </button>
      </div>
    </section>
  );
}
