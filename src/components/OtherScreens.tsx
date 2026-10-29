import { ArrowLeft, ArrowRight, FlaskConical, Plus, Search } from 'lucide-react';
import {
  type Dispatch,
  type FormEvent,
  type SetStateAction,
  useEffect,
  useRef,
  useState,
} from 'react';
import type { TestParameters } from '../model';
import { PressureNotation } from './PressureNotation';
import './OtherScreens.css';

interface TestsScreenProps {
  empty: boolean;
  draftAvailable: boolean;
  activeParameters: TestParameters | null;
  onNew: () => void;
  onOpen: () => void;
  onHistory: () => void;
}

export function TestsScreen({
  empty,
  draftAvailable,
  activeParameters,
  onNew,
  onOpen,
  onHistory,
}: TestsScreenProps) {
  const [query, setQuery] = useState('');
  const matches = 'демо-001 образец клапана'.includes(query.trim().toLocaleLowerCase('ru'));

  return (
    <section className="workpage" aria-labelledby="tests-title">
      <div className="workpage-heading">
        <div>
          <h1 id="tests-title">Испытания</h1>
        </div>
        <div className="action-with-help">
          {activeParameters !== null && (
            <span id="new-test-block-reason">
              В примере уже есть активное испытание. Вернитесь к нему ниже.
            </span>
          )}
          <button
            className="work-button work-button-primary"
            type="button"
            onClick={onNew}
            disabled={activeParameters !== null}
            aria-describedby={activeParameters !== null ? 'new-test-block-reason' : undefined}
          >
            <Plus aria-hidden="true" size={18} />{' '}
            {draftAvailable ? 'Продолжить подготовку' : 'Новое испытание'}
          </button>
        </div>
      </div>

      {activeParameters !== null && (
        <div className="active-example">
          <div className="active-example-icon">
            <FlaskConical aria-hidden="true" size={20} />
          </div>
          <div className="active-example-details">
            <span className="status-label">
              <span className="status-dot" /> Пример активного прохода
            </span>
            <strong>
              {activeParameters.id} · {activeParameters.valve}
            </strong>
            <span>
              {activeParameters.direction === 'pressure' ? 'Нагнетание' : 'Разрежение'} ·{' '}
              {activeParameters.pmax.toLocaleString('ru-RU')} Па ·{' '}
              {activeParameters.actuator ? 'с приводом' : 'без привода'}
            </span>
          </div>
          <button className="work-button work-button-secondary" type="button" onClick={onOpen}>
            Вернуться к испытанию <ArrowRight aria-hidden="true" size={16} />
          </button>
        </div>
      )}

      <div className="work-surface history-surface">
        <div className="surface-heading">
          <div>
            <h2>История испытаний</h2>
          </div>
          {!empty && (
            <label className="history-search">
              <Search aria-hidden="true" size={17} />
              <span className="visually-hidden">Поиск по номеру или изделию</span>
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Номер или изделие"
              />
            </label>
          )}
        </div>
        {empty ? (
          <div className="empty-example">
            <p>История пуста</p>
          </div>
        ) : matches ? (
          <div className="table-scroll">
            <table className="work-table">
              <thead>
                <tr>
                  <th>Испытание</th>
                  <th>Изделие</th>
                  <th>Направление</th>
                  <th>
                    <PressureNotation />
                  </th>
                  <th>Привод</th>
                  <th>Ход</th>
                  <th>Сохранение</th>
                  <th>
                    <span className="visually-hidden">Действие</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <strong>ДЕМО-001</strong>
                  </td>
                  <td>Образец клапана</td>
                  <td>Нагнетание</td>
                  <td>10 000 Па</td>
                  <td>Нет</td>
                  <td>Завершён в примере</td>
                  <td>Пример: сохранено</td>
                  <td>
                    <button className="table-action" type="button" onClick={onHistory}>
                      Открыть
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        ) : (
          <div className="empty-example">
            <p>По запросу «{query}» ничего не найдено.</p>
            <button className="table-action" type="button" onClick={() => setQuery('')}>
              Сбросить поиск
            </button>
          </div>
        )}
      </div>
    </section>
  );
}

type Direction = TestParameters['direction'];
export interface Draft {
  valve: string;
  area: string;
  pmax: string;
  direction: Direction;
  actuator: boolean;
  voltage: '24' | '220';
  openSeconds: string;
  closeSeconds: string;
  measurementSeconds: string;
}
type Errors = Partial<
  Record<'valve' | 'area' | 'pmax' | 'openSeconds' | 'closeSeconds' | 'measurementSeconds', string>
>;

function positiveNumber(value: string): number | null {
  const normalized = value.trim().replace(',', '.');
  if (!/^\d+(?:\.\d+)?$/.test(normalized)) return null;
  const parsed = Number(normalized);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

export function draftFrom(parameters: TestParameters): Draft {
  return {
    valve: parameters.valve,
    area: String(parameters.area),
    pmax: String(parameters.pmax),
    direction: parameters.direction,
    actuator: parameters.actuator,
    voltage: String(parameters.voltage) === '220' ? '220' : '24',
    openSeconds: String(parameters.openSeconds),
    closeSeconds: String(parameters.closeSeconds),
    measurementSeconds: String(parameters.measurementSeconds),
  };
}

interface ParametersScreenProps {
  initial: TestParameters;
  draft: Draft;
  setDraft: Dispatch<SetStateAction<Draft>>;
  onBack: () => void;
  onContinue: (parameters: TestParameters) => void;
}

export function ParametersScreen({
  initial,
  draft,
  setDraft,
  onBack,
  onContinue,
}: ParametersScreenProps) {
  const [errors, setErrors] = useState<Errors>({});
  const [focusRequest, setFocusRequest] = useState<{ field: keyof Errors } | null>(null);
  const formRef = useRef<HTMLFormElement>(null);

  useEffect(() => {
    if (focusRequest !== null) {
      formRef.current?.querySelector<HTMLInputElement>(`#parameter-${focusRequest.field}`)?.focus();
    }
  }, [focusRequest]);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const area = positiveNumber(draft.area);
    const pmax = positiveNumber(draft.pmax);
    const measurementSeconds = positiveNumber(draft.measurementSeconds);
    const openSeconds = positiveNumber(draft.openSeconds);
    const closeSeconds = positiveNumber(draft.closeSeconds);
    const nextErrors: Errors = {};
    if (!draft.valve.trim()) nextErrors.valve = 'Укажите обозначение или наименование клапана.';
    if (area === null) nextErrors.area = 'Введите площадь больше нуля, м².';
    if (pmax === null) nextErrors.pmax = 'Введите максимальное давление больше нуля, Па.';
    if (measurementSeconds === null)
      nextErrors.measurementSeconds = 'Введите длительность больше нуля, с.';
    if (draft.actuator && openSeconds === null)
      nextErrors.openSeconds = 'Введите длительность команды больше нуля, с.';
    if (draft.actuator && closeSeconds === null)
      nextErrors.closeSeconds = 'Введите длительность команды больше нуля, с.';
    setErrors(nextErrors);
    const firstInvalid = (
      ['valve', 'area', 'pmax', 'measurementSeconds', 'openSeconds', 'closeSeconds'] as const
    ).find((name) => nextErrors[name] !== undefined);
    if (firstInvalid !== undefined) {
      setFocusRequest({ field: firstInvalid });
    }
    if (
      area === null ||
      pmax === null ||
      measurementSeconds === null ||
      !draft.valve.trim() ||
      (draft.actuator && (openSeconds === null || closeSeconds === null))
    )
      return;
    onContinue({
      ...initial,
      valve: draft.valve.trim(),
      area,
      pmax,
      measurementSeconds,
      direction: draft.direction,
      actuator: draft.actuator,
      voltage: draft.voltage === '24' ? 24 : 220,
      openSeconds: openSeconds ?? initial.openSeconds,
      closeSeconds: closeSeconds ?? initial.closeSeconds,
    });
  }

  function inputError(name: keyof Errors) {
    return errors[name]
      ? {
          id: `parameter-${name}`,
          'aria-invalid': true as const,
          'aria-describedby': `${name}-error`,
        }
      : { id: `parameter-${name}` };
  }

  return (
    <section className="workpage workpage-narrow" aria-labelledby="parameters-title">
      <div className="workpage-heading">
        <div>
          <h1 id="parameters-title">Параметры испытания</h1>
        </div>
      </div>
      <form ref={formRef} onSubmit={submit} noValidate>
        <div className="work-surface form-surface">
          <div className="form-section">
            <div className="section-heading">
              <h2>Изделие</h2>
            </div>
            <div className="form-grid">
              <label className="form-field form-field-wide">
                <span>Обозначение или наименование клапана</span>
                <input
                  className="work-input"
                  value={draft.valve}
                  {...inputError('valve')}
                  onChange={(event) => setDraft({ ...draft, valve: event.target.value })}
                  placeholder="Например, клапан К-01"
                />
                {errors.valve && (
                  <small className="field-error" id="valve-error">
                    {errors.valve}
                  </small>
                )}
              </label>
              <label className="form-field">
                <span>
                  Площадь клапана, м<sup>2</sup>
                </span>
                <input
                  className="work-input"
                  aria-label="Площадь клапана, квадратные метры"
                  inputMode="decimal"
                  value={draft.area}
                  {...inputError('area')}
                  onChange={(event) => setDraft({ ...draft, area: event.target.value })}
                />
                {errors.area && (
                  <small className="field-error" id="area-error">
                    {errors.area}
                  </small>
                )}
              </label>
            </div>
          </div>
          <div className="form-section">
            <div className="section-heading">
              <h2>Рабочая точка</h2>
              <p>Одна точка при максимальном давлении производителя</p>
            </div>
            <div className="form-grid">
              <label className="form-field">
                <span>
                  <PressureNotation /> производителя, Па
                </span>
                <input
                  className="work-input"
                  inputMode="decimal"
                  value={draft.pmax}
                  {...inputError('pmax')}
                  onChange={(event) => setDraft({ ...draft, pmax: event.target.value })}
                />
                {errors.pmax && (
                  <small className="field-error" id="pmax-error">
                    {errors.pmax}
                  </small>
                )}
              </label>
              <label className="form-field">
                <span>Интервал измерения, с</span>
                <input
                  className="work-input"
                  inputMode="decimal"
                  value={draft.measurementSeconds}
                  {...inputError('measurementSeconds')}
                  onChange={(event) =>
                    setDraft({ ...draft, measurementSeconds: event.target.value })
                  }
                />
                {errors.measurementSeconds && (
                  <small className="field-error" id="measurementSeconds-error">
                    {errors.measurementSeconds}
                  </small>
                )}
              </label>
              <fieldset className="form-field form-field-wide">
                <legend>Направление испытания</legend>
                <div className="choice-row">
                  <label>
                    <input
                      type="radio"
                      name="direction"
                      checked={draft.direction === 'pressure'}
                      onChange={() => setDraft({ ...draft, direction: 'pressure' })}
                    />{' '}
                    Нагнетание
                  </label>
                  <label>
                    <input
                      type="radio"
                      name="direction"
                      checked={draft.direction === 'vacuum'}
                      onChange={() => setDraft({ ...draft, direction: 'vacuum' })}
                    />{' '}
                    Разрежение
                  </label>
                </div>
                <small>
                  Диафрагма № {draft.direction === 'pressure' ? '1' : '2'}. Патрубок переключается
                  вручную.
                </small>
              </fieldset>
            </div>
          </div>
          <div className="form-section">
            <div className="section-heading">
              <h2>Привод клапана</h2>
            </div>
            <fieldset className="form-field">
              <legend className="visually-hidden">Наличие привода</legend>
              <div className="choice-row">
                <label>
                  <input
                    type="radio"
                    name="actuator"
                    checked={!draft.actuator}
                    onChange={() => setDraft({ ...draft, actuator: false })}
                  />{' '}
                  Нет
                </label>
                <label>
                  <input
                    type="radio"
                    name="actuator"
                    checked={draft.actuator}
                    onChange={() => setDraft({ ...draft, actuator: true })}
                  />{' '}
                  Есть
                </label>
              </div>
            </fieldset>
            {draft.actuator && (
              <div className="form-grid actuator-grid">
                <label className="form-field">
                  <span>Внешняя цепь, В</span>
                  <select
                    className="work-input"
                    value={draft.voltage}
                    onChange={(event) =>
                      setDraft({ ...draft, voltage: event.target.value === '220' ? '220' : '24' })
                    }
                  >
                    <option value="24">24</option>
                    <option value="220">220</option>
                  </select>
                </label>
                <label className="form-field">
                  <span>Команда открытия, с</span>
                  <input
                    className="work-input"
                    inputMode="decimal"
                    value={draft.openSeconds}
                    {...inputError('openSeconds')}
                    onChange={(event) => setDraft({ ...draft, openSeconds: event.target.value })}
                  />
                  {errors.openSeconds && (
                    <small className="field-error" id="openSeconds-error">
                      {errors.openSeconds}
                    </small>
                  )}
                </label>
                <label className="form-field">
                  <span>Команда закрытия, с</span>
                  <input
                    className="work-input"
                    inputMode="decimal"
                    value={draft.closeSeconds}
                    {...inputError('closeSeconds')}
                    onChange={(event) => setDraft({ ...draft, closeSeconds: event.target.value })}
                  />
                  {errors.closeSeconds && (
                    <small className="field-error" id="closeSeconds-error">
                      {errors.closeSeconds}
                    </small>
                  )}
                </label>
                <p className="field-help form-field-wide">
                  Длительность подачи команды не является измеренным временем перемещения.
                </p>
              </div>
            )}
          </div>
        </div>
        <div className="work-actions">
          <button className="work-button work-button-secondary" type="button" onClick={onBack}>
            <ArrowLeft aria-hidden="true" size={16} /> К испытаниям
          </button>
          <button className="work-button work-button-primary" type="submit">
            Проверить готовность <ArrowRight aria-hidden="true" size={16} />
          </button>
        </div>
      </form>
    </section>
  );
}

export function ActiveParametersScreen({
  parameters,
  onReturn,
}: {
  parameters: TestParameters;
  onReturn: () => void;
}) {
  return (
    <section className="workpage workpage-narrow" aria-labelledby="active-parameters-title">
      <div className="workpage-heading">
        <div>
          <h1 id="active-parameters-title">Параметры испытания</h1>
          <p>Текущее испытание · только просмотр</p>
        </div>
      </div>
      <div className="work-surface result-section">
        <dl className="result-details">
          <div>
            <dt>Испытание</dt>
            <dd>{parameters.id}</dd>
          </div>
          <div>
            <dt>Изделие</dt>
            <dd>{parameters.valve}</dd>
          </div>
          <div>
            <dt>Площадь клапана</dt>
            <dd>
              {parameters.area.toLocaleString('ru-RU')} м<sup>2</sup>
            </dd>
          </div>
          <div>
            <dt>
              Давление <PressureNotation />
            </dt>
            <dd>{parameters.pmax.toLocaleString('ru-RU')} Па</dd>
          </div>
          <div>
            <dt>Интервал измерения</dt>
            <dd>{parameters.measurementSeconds} с</dd>
          </div>
          <div>
            <dt>Направление</dt>
            <dd>
              {parameters.direction === 'pressure'
                ? 'Нагнетание · диафрагма №1'
                : 'Разрежение · диафрагма №2'}
            </dd>
          </div>
          <div>
            <dt>Привод</dt>
            <dd>{parameters.actuator ? 'Есть' : 'Нет'}</dd>
          </div>
          {parameters.actuator && (
            <>
              <div>
                <dt>Внешняя цепь</dt>
                <dd>{parameters.voltage} В</dd>
              </div>
              <div>
                <dt>Команда открытия</dt>
                <dd>{parameters.openSeconds} с</dd>
              </div>
              <div>
                <dt>Команда закрытия</dt>
                <dd>{parameters.closeSeconds} с</dd>
              </div>
            </>
          )}
        </dl>
      </div>
      <div className="work-actions">
        <button className="work-button work-button-primary" type="button" onClick={onReturn}>
          Вернуться к испытанию <ArrowRight aria-hidden="true" size={16} />
        </button>
      </div>
    </section>
  );
}

interface ReadinessScreenProps {
  parameters: TestParameters;
  onBack: () => void;
  onStart: () => void;
}

export function ReadinessScreen({ parameters, onBack, onStart }: ReadinessScreenProps) {
  const [valveConfirmed, setValveConfirmed] = useState(false);
  const [connectionConfirmed, setConnectionConfirmed] = useState(false);
  const canStart = valveConfirmed && connectionConfirmed;
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
          <strong>{parameters.pmax.toLocaleString('ru-RU')} Па</strong>
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
          <p>Подготовленный пример состояния</p>
        </div>
        <ul className="checks-list">
          <li>
            <span>Связь и актуальность показаний</span>
            <strong>Готово</strong>
          </li>
          <li>
            <span>Аварийный вход и конфигурация</span>
            <strong>Готово</strong>
          </li>
          <li>
            <span>Необходимое оборудование</span>
            <strong>Готово</strong>
          </li>
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
          {!canStart && <span>Отметьте оба подтверждения оператора.</span>}
          <button
            className="work-button work-button-primary"
            type="button"
            disabled={!canStart}
            onClick={onStart}
          >
            Перейти к проведению <ArrowRight aria-hidden="true" size={16} />
          </button>
        </div>
      </div>
    </section>
  );
}

interface ResultScreenProps {
  parameters: TestParameters;
  saveError: boolean;
  interrupted: boolean;
  active: boolean;
  draftAvailable: boolean;
  onList: () => void;
  onNew: () => void;
}

export function ResultScreen({
  parameters,
  saveError,
  interrupted,
  active,
  draftAvailable,
  onList,
  onNew,
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
          <strong>{interrupted ? 'Прерван в примере' : 'Завершён в примере'}</strong>
        </div>
        <div>
          <span>Данные</span>
          <strong>{interrupted ? 'Пример: частичные данные' : 'Пример: данные доступны'}</strong>
        </div>
        <div>
          <span>Сохранение</span>
          <strong className={saveError ? 'error-text' : ''}>
            {saveError ? 'Ошибка сохранения · пример' : 'Пример: сохранено'}
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
                {parameters.area.toLocaleString('ru-RU')} м<sup>2</sup>
              </dd>
            </div>
            <div>
              <dt>
                Давление <PressureNotation />
              </dt>
              <dd>{parameters.pmax.toLocaleString('ru-RU')} Па</dd>
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
              <dt>Интервал измерения</dt>
              <dd>{parameters.measurementSeconds} с</dd>
            </div>
          </dl>
        </div>
        <div className="work-surface result-section">
          <div className="section-heading">
            <h2>Результаты расчёта</h2>
            <p>В макете не рассчитываются</p>
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
