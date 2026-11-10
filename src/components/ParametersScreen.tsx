import { ArrowLeft, ArrowRight } from 'lucide-react';
import {
  type Dispatch,
  type FormEvent,
  type ReactNode,
  type SetStateAction,
  useEffect,
  useRef,
  useState,
} from 'react';
import { exactPositiveDecimal, trimDemoValve } from '../desktop/demo-input';
import { formatNumber } from '../lib/format';
import type { TestParameters } from '../model';
import { PressureNotation } from './PressureNotation';
import './screens.css';

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
  preOpenStabilizationSeconds: string;
  postCloseStabilizationSeconds: string;
  postCloseStabilizationRetries: string;
}
type Errors = Partial<
  Record<
    | 'valve'
    | 'area'
    | 'pmax'
    | 'openSeconds'
    | 'closeSeconds'
    | 'measurementSeconds'
    | 'preOpenStabilizationSeconds'
    | 'postCloseStabilizationSeconds'
    | 'postCloseStabilizationRetries',
    string
  >
>;

function RequiredLabel({ id, children }: { id?: string; children: ReactNode }) {
  return (
    <span>
      <span id={id}>{children}</span>{' '}
      <span className="required-marker" aria-hidden="true">
        *
      </span>
    </span>
  );
}

function positiveNumber(value: string): number | null {
  const normalized = value.trim().replace(',', '.');
  if (!/^\d+(?:\.\d+)?$/.test(normalized)) return null;
  const parsed = Number(normalized);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

function wholeNumber(value: string, min: number, max: number): number | null {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const number = Number(trimmed);
  return Number.isSafeInteger(number) && number >= min && number <= max ? number : null;
}

function liveFieldError(name: 'valve' | 'area' | 'pmax', value: string): string | undefined {
  if (name === 'valve') {
    const trimmed = trimDemoValve(value);
    if (!trimmed) return 'Укажите обозначение или наименование клапана.';
    return Array.from(trimmed).length > 200 || /\p{Surrogate}/u.test(value)
      ? 'Укажите не более 200 корректных символов.'
      : undefined;
  }
  const result = exactPositiveDecimal(value);
  if (result.error === 'precision') return 'Число нельзя передать без потери точности.';
  if (result.error === 'invalid')
    return name === 'area'
      ? 'Введите площадь больше нуля, м².'
      : 'Введите максимальное давление больше нуля, Па.';
  return undefined;
}

function draftNumber(value: number): string {
  return new Intl.NumberFormat('en-US', {
    useGrouping: false,
    maximumSignificantDigits: 21,
  }).format(value);
}

export function draftFrom(parameters: TestParameters): Draft {
  return {
    valve: parameters.valve,
    area: draftNumber(parameters.area),
    pmax: draftNumber(parameters.pmax),
    direction: parameters.direction,
    actuator: parameters.actuator,
    voltage: String(parameters.voltage) === '220' ? '220' : '24',
    openSeconds: draftNumber(parameters.openSeconds),
    closeSeconds: draftNumber(parameters.closeSeconds),
    measurementSeconds: draftNumber(parameters.measurementSeconds),
    preOpenStabilizationSeconds: draftNumber(parameters.preOpenStabilizationSeconds),
    postCloseStabilizationSeconds: draftNumber(parameters.postCloseStabilizationSeconds),
    postCloseStabilizationRetries:
      parameters.postCloseStabilizationRetries === null
        ? ''
        : String(parameters.postCloseStabilizationRetries),
  };
}

interface ParametersScreenProps {
  initial: TestParameters;
  draft: Draft;
  setDraft: Dispatch<SetStateAction<Draft>>;
  onBack: () => void;
  onContinue: (parameters: TestParameters & { postCloseStabilizationRetries: number }) => void;
  live?: boolean;
  pending?: boolean;
  unavailable?: boolean;
}

export function ParametersScreen({
  initial,
  draft,
  setDraft,
  onBack,
  onContinue,
  live = false,
  pending = false,
  unavailable = false,
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
    const areaResult = live ? exactPositiveDecimal(draft.area) : undefined;
    const pressureResult = live ? exactPositiveDecimal(draft.pmax) : undefined;
    const area = live ? (areaResult?.value ?? null) : positiveNumber(draft.area);
    const pmax = live ? (pressureResult?.value ?? null) : positiveNumber(draft.pmax);
    const measurementSeconds = wholeNumber(draft.measurementSeconds, 60, 300);
    const preOpenStabilizationSeconds = wholeNumber(draft.preOpenStabilizationSeconds, 10, 100);
    const postCloseStabilizationSeconds = wholeNumber(draft.postCloseStabilizationSeconds, 10, 100);
    const postCloseStabilizationRetries = wholeNumber(draft.postCloseStabilizationRetries, 0, 100);
    const openSeconds = positiveNumber(draft.openSeconds);
    const closeSeconds = positiveNumber(draft.closeSeconds);
    const nextErrors: Errors = {};
    const valve = live ? trimDemoValve(draft.valve) : draft.valve.trim();
    if (!valve) nextErrors.valve = 'Укажите обозначение или наименование клапана.';
    if (area === null) nextErrors.area = 'Введите площадь больше нуля, м².';
    if (pmax === null) nextErrors.pmax = 'Введите максимальное давление больше нуля, Па.';
    if (measurementSeconds === null)
      nextErrors.measurementSeconds = 'Введите целое число от 60 до 300 с.';
    if (preOpenStabilizationSeconds === null)
      nextErrors.preOpenStabilizationSeconds = 'Введите целое число от 10 до 100 с.';
    if (postCloseStabilizationSeconds === null)
      nextErrors.postCloseStabilizationSeconds = 'Введите целое число от 10 до 100 с.';
    if (postCloseStabilizationRetries === null)
      nextErrors.postCloseStabilizationRetries = 'Введите целое число от 0 до 100.';
    if (areaResult?.error === 'precision')
      nextErrors.area = 'Число нельзя передать без потери точности.';
    if (pressureResult?.error === 'precision')
      nextErrors.pmax = 'Число нельзя передать без потери точности.';
    if (live) {
      for (const field of ['valve', 'area', 'pmax'] as const) {
        const error = liveFieldError(field, draft[field]);
        if (error) nextErrors[field] = error;
        else delete nextErrors[field];
      }
    }
    if (draft.actuator && openSeconds === null)
      nextErrors.openSeconds = 'Введите длительность команды больше нуля, с.';
    if (draft.actuator && closeSeconds === null)
      nextErrors.closeSeconds = 'Введите длительность команды больше нуля, с.';
    setErrors(nextErrors);
    const firstInvalid = (
      [
        'valve',
        'area',
        'pmax',
        'measurementSeconds',
        'preOpenStabilizationSeconds',
        'postCloseStabilizationSeconds',
        'postCloseStabilizationRetries',
        'openSeconds',
        'closeSeconds',
      ] as const
    ).find((name) => nextErrors[name] !== undefined);
    if (firstInvalid !== undefined) {
      setFocusRequest({ field: firstInvalid });
    }
    if (
      area === null ||
      pmax === null ||
      measurementSeconds === null ||
      preOpenStabilizationSeconds === null ||
      postCloseStabilizationSeconds === null ||
      postCloseStabilizationRetries === null ||
      nextErrors.valve !== undefined ||
      (live && (draft.direction !== 'pressure' || draft.actuator)) ||
      (draft.actuator && (openSeconds === null || closeSeconds === null))
    )
      return;
    onContinue({
      ...initial,
      valve,
      area,
      pmax,
      measurementSeconds,
      preOpenStabilizationSeconds,
      postCloseStabilizationSeconds,
      postCloseStabilizationRetries,
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
          required: true,
          'aria-invalid': true as const,
          'aria-describedby': `${name}-error`,
        }
      : { id: `parameter-${name}`, required: true };
  }

  function updateInput(name: keyof Errors, value: string) {
    setDraft({ ...draft, [name]: value });
    if (
      name === 'measurementSeconds' ||
      name === 'preOpenStabilizationSeconds' ||
      name === 'postCloseStabilizationSeconds' ||
      name === 'postCloseStabilizationRetries'
    ) {
      const min =
        name === 'measurementSeconds' ? 60 : name === 'postCloseStabilizationRetries' ? 0 : 10;
      const max = name === 'measurementSeconds' ? 300 : 100;
      if (wholeNumber(value, min, max) !== null)
        setErrors((current) => ({ ...current, [name]: undefined }));
      return;
    }
    if (live && (name === 'valve' || name === 'area' || name === 'pmax')) {
      setErrors((current) =>
        current[name] ? { ...current, [name]: liveFieldError(name, value) } : current,
      );
      return;
    }
    const valid =
      name === 'valve'
        ? value.trim() !== '' &&
          (!live || (Array.from(value.trim()).length <= 200 && !/\p{Surrogate}/u.test(value)))
        : live && (name === 'area' || name === 'pmax')
          ? exactPositiveDecimal(value).value !== undefined
          : positiveNumber(value) !== null;
    if (valid) {
      setErrors((current) => ({ ...current, [name]: undefined }));
    }
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
                <RequiredLabel id="valve-label">Обозначение или наименование клапана</RequiredLabel>
                <input
                  className="work-input"
                  aria-labelledby="valve-label"
                  value={draft.valve}
                  {...inputError('valve')}
                  onChange={(event) => updateInput('valve', event.target.value)}
                  placeholder="Например, клапан К-01"
                />
                {errors.valve && (
                  <small className="field-error" id="valve-error">
                    {errors.valve}
                  </small>
                )}
              </label>
              <label className="form-field">
                <RequiredLabel>
                  Площадь клапана, м<sup>2</sup>
                </RequiredLabel>
                <input
                  className="work-input"
                  aria-label="Площадь клапана, квадратные метры"
                  inputMode="decimal"
                  value={draft.area}
                  {...inputError('area')}
                  onChange={(event) => updateInput('area', event.target.value)}
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
                <RequiredLabel id="pmax-label">
                  <PressureNotation /> производителя, Па
                </RequiredLabel>
                <input
                  className="work-input"
                  aria-labelledby="pmax-label"
                  inputMode="decimal"
                  value={draft.pmax}
                  {...inputError('pmax')}
                  onChange={(event) => updateInput('pmax', event.target.value)}
                />
                {errors.pmax && (
                  <small className="field-error" id="pmax-error">
                    {errors.pmax}
                  </small>
                )}
              </label>
              <label className="form-field">
                <RequiredLabel id="measurementSeconds-label">
                  Длительность измерения, с
                </RequiredLabel>
                <input
                  className="work-input"
                  aria-labelledby="measurementSeconds-label"
                  inputMode="decimal"
                  value={draft.measurementSeconds}
                  {...inputError('measurementSeconds')}
                  onChange={(event) => updateInput('measurementSeconds', event.target.value)}
                />
                {errors.measurementSeconds && (
                  <small className="field-error" id="measurementSeconds-error">
                    {errors.measurementSeconds}
                  </small>
                )}
              </label>
              <label className="form-field">
                <RequiredLabel id="preOpenStabilizationSeconds-label">
                  Стабилизация перед открытием клапана, с
                </RequiredLabel>
                <input
                  className="work-input"
                  aria-labelledby="preOpenStabilizationSeconds-label"
                  inputMode="decimal"
                  value={draft.preOpenStabilizationSeconds}
                  {...inputError('preOpenStabilizationSeconds')}
                  onChange={(event) =>
                    updateInput('preOpenStabilizationSeconds', event.target.value)
                  }
                />
                {errors.preOpenStabilizationSeconds && (
                  <small className="field-error" id="preOpenStabilizationSeconds-error">
                    {errors.preOpenStabilizationSeconds}
                  </small>
                )}
              </label>
              <label className="form-field">
                <RequiredLabel id="postCloseStabilizationSeconds-label">
                  Стабилизация после закрытия клапана, с
                </RequiredLabel>
                <input
                  className="work-input"
                  aria-labelledby="postCloseStabilizationSeconds-label"
                  inputMode="decimal"
                  value={draft.postCloseStabilizationSeconds}
                  {...inputError('postCloseStabilizationSeconds')}
                  onChange={(event) =>
                    updateInput('postCloseStabilizationSeconds', event.target.value)
                  }
                />
                {errors.postCloseStabilizationSeconds && (
                  <small className="field-error" id="postCloseStabilizationSeconds-error">
                    {errors.postCloseStabilizationSeconds}
                  </small>
                )}
              </label>
              <label className="form-field">
                <RequiredLabel id="postCloseStabilizationRetries-label">
                  Допустимое число повторов стабилизации после закрытия
                </RequiredLabel>
                <input
                  className="work-input"
                  aria-labelledby="postCloseStabilizationRetries-label"
                  inputMode="numeric"
                  value={draft.postCloseStabilizationRetries}
                  {...inputError('postCloseStabilizationRetries')}
                  onChange={(event) =>
                    updateInput('postCloseStabilizationRetries', event.target.value)
                  }
                />
                {errors.postCloseStabilizationRetries && (
                  <small className="field-error" id="postCloseStabilizationRetries-error">
                    {errors.postCloseStabilizationRetries}
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
                      disabled={live}
                      onChange={() => setDraft({ ...draft, direction: 'vacuum' })}
                    />{' '}
                    Разрежение {live && <small aria-hidden="true">Недоступно</small>}
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
                    disabled={live}
                    onChange={() => setDraft({ ...draft, actuator: true })}
                  />{' '}
                  Есть {live && <small aria-hidden="true">Недоступно</small>}
                </label>
              </div>
            </fieldset>
            {draft.actuator && (
              <div className="form-grid actuator-grid">
                <label className="form-field">
                  <RequiredLabel id="voltage-label">Внешняя цепь, В</RequiredLabel>
                  <select
                    className="work-input"
                    aria-labelledby="voltage-label"
                    required
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
                  <RequiredLabel id="openSeconds-label">Команда открытия, с</RequiredLabel>
                  <input
                    className="work-input"
                    aria-labelledby="openSeconds-label"
                    inputMode="decimal"
                    value={draft.openSeconds}
                    {...inputError('openSeconds')}
                    onChange={(event) => updateInput('openSeconds', event.target.value)}
                  />
                  {errors.openSeconds && (
                    <small className="field-error" id="openSeconds-error">
                      {errors.openSeconds}
                    </small>
                  )}
                </label>
                <label className="form-field">
                  <RequiredLabel id="closeSeconds-label">Команда закрытия, с</RequiredLabel>
                  <input
                    className="work-input"
                    aria-labelledby="closeSeconds-label"
                    inputMode="decimal"
                    value={draft.closeSeconds}
                    {...inputError('closeSeconds')}
                    onChange={(event) => updateInput('closeSeconds', event.target.value)}
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
        <p className="required-key">* — обязательное поле</p>
        <div className="work-actions">
          <button className="work-button work-button-secondary" type="button" onClick={onBack}>
            <ArrowLeft aria-hidden="true" size={16} /> К испытаниям
          </button>
          <button
            className="work-button work-button-primary"
            type="submit"
            disabled={pending || unavailable}
          >
            {pending ? 'Создание испытания…' : 'Проверить готовность'}{' '}
            <ArrowRight aria-hidden="true" size={16} />
          </button>
        </div>
      </form>
    </section>
  );
}

export function ActiveParametersScreen({ parameters }: { parameters: TestParameters }) {
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
            <dt>Повторы стабилизации после закрытия</dt>
            <dd>{parameters.postCloseStabilizationRetries ?? 'Не указано'}</dd>
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
                <dd>{formatNumber(parameters.openSeconds)} с</dd>
              </div>
              <div>
                <dt>Команда закрытия</dt>
                <dd>{formatNumber(parameters.closeSeconds)} с</dd>
              </div>
            </>
          )}
        </dl>
      </div>
    </section>
  );
}
