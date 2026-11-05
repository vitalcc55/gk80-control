import { ArrowLeft, ArrowRight } from 'lucide-react';
import { useState } from 'react';
import type { Readiness } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import { formatNumber } from '../lib/format';
import type { TestParameters } from '../model';
import './screens.css';

interface ReadinessScreenProps {
  parameters: TestParameters;
  autoCheckFailed: boolean;
  onBack: () => void;
  onStart: (confirmations: { valveClosed: boolean; connectionConfirmed: boolean }) => void;
  readiness?: Readiness;
  pending?: boolean;
  stale?: boolean;
}

export function ReadinessScreen({
  parameters,
  autoCheckFailed,
  onBack,
  onStart,
  readiness,
  pending = false,
  stale = false,
}: ReadinessScreenProps) {
  const [valveConfirmed, setValveConfirmed] = useState(false);
  const [connectionConfirmed, setConnectionConfirmed] = useState(false);
  const canStart =
    valveConfirmed &&
    connectionConfirmed &&
    !autoCheckFailed &&
    (readiness?.ready ?? true) &&
    !pending;
  return (
    <section className="workpage workpage-narrow" aria-labelledby="readiness-title">
      <div className="workpage-heading">
        <div>
          <h1 id="readiness-title">Готовность</h1>
        </div>
      </div>
      <div className="parameter-summary">
        <div>
          <span>Изделие</span>
          <strong>{parameters.valve}</strong>
        </div>
        <div>
          <span>Рабочая точка</span>
          <strong>{formatNumber(parameters.pmax)} Па</strong>
        </div>
        <div>
          <span>Направление</span>
          <strong>{parameters.direction === 'pressure' ? 'Нагнетание' : 'Разрежение'}</strong>
        </div>
        <div>
          <span>Привод</span>
          <strong>{parameters.actuator ? 'Есть' : 'Нет'}</strong>
        </div>
      </div>
      <div className="work-surface readiness-surface">
        <div className="section-heading">
          <h2>Автоматические проверки</h2>
          <p>{readiness ? 'Проверки демонстрационного ядра' : 'Подготовленный пример состояния'}</p>
        </div>
        <ul className="checks-list">
          {readiness ? (
            readiness.checks?.map((check) => (
              <li key={check.code}>
                <span>{check.label}</span>
                <strong className={check.ready ? undefined : 'error-text'}>
                  {stale ? 'Не подтверждено' : check.ready ? 'Готово' : 'Не готово'}
                </strong>
              </li>
            ))
          ) : (
            <>
              <li>
                <span>Связь и актуальность показаний</span>
                <strong className={autoCheckFailed ? 'error-text' : undefined}>
                  {autoCheckFailed ? 'Нет связи · пример' : 'Готово'}
                </strong>
              </li>
              <li>
                <span>Аварийный вход и конфигурация</span>
                <strong>Готово</strong>
              </li>
              <li>
                <span>Необходимое оборудование</span>
                <strong>Готово</strong>
              </li>
            </>
          )}
        </ul>
        <div className="section-heading checks-heading">
          <h2>Подтверждения оператора</h2>
        </div>
        <div className="confirm-list">
          <label>
            <input
              type="checkbox"
              checked={valveConfirmed}
              onChange={(event) => setValveConfirmed(event.target.checked)}
            />
            <span>
              <strong>Клапан закрыт</strong>
              <small>Визуально проверьте положение клапана.</small>
            </span>
          </label>
          <label>
            <input
              type="checkbox"
              checked={connectionConfirmed}
              onChange={(event) => setConnectionConfirmed(event.target.checked)}
            />
            <span>
              <strong>Подключение соответствует направлению</strong>
              <small>
                Патрубок переключается вручную; диафрагма №{' '}
                {parameters.direction === 'pressure' ? '1' : '2'}.
              </small>
            </span>
          </label>
        </div>
      </div>
      <div className="work-actions">
        <button className="work-button work-button-secondary" type="button" onClick={onBack}>
          <ArrowLeft aria-hidden="true" size={16} /> К параметрам
        </button>
        <div className="action-with-help">
          {!canStart && (
            <span>
              {stale
                ? 'Связь или готовность не подтверждены.'
                : pending
                  ? 'Ожидаем ответ ядра.'
                  : autoCheckFailed
                    ? 'Автоматическая проверка не пройдена.'
                    : 'Отметьте оба подтверждения оператора.'}
            </span>
          )}
          <button
            className="work-button work-button-primary"
            type="button"
            disabled={!canStart}
            onClick={() => onStart({ valveClosed: valveConfirmed, connectionConfirmed })}
          >
            {pending ? 'Ожидание ядра…' : 'Перейти к проведению'}{' '}
            <ArrowRight aria-hidden="true" size={16} />
          </button>
        </div>
      </div>
    </section>
  );
}
