import { Activity, ArrowLeft, ArrowRight, Check, FileText, ListChecks, Pause } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { MeasurementChart } from './components/MeasurementChart';
import {
  ActiveParametersScreen,
  draftFrom,
  ParametersScreen,
  ReadinessScreen,
  ResultScreen,
  TestsScreen,
} from './components/OtherScreens';
import { PressureNotation } from './components/PressureNotation';
import { Button } from './components/ui/button';
import { demoSteps, fixtureSource, prepareRunDisplay } from './fixtureAdapter';
import type { Fixture, FixtureId, Screen, TestParameters } from './model';

const headings: Record<Screen, string> = {
  tests: 'Испытания',
  parameters: 'Параметры',
  readiness: 'Готовность',
  run: 'Проведение',
  result: 'Результат',
};

function number(value: number): string {
  return new Intl.NumberFormat('ru-RU', { maximumSignificantDigits: 12 }).format(value);
}

function initialFixture(): Fixture {
  const query = new URLSearchParams(window.location.search);
  const fixture = fixtureSource.find(query.get('fixture')) ?? fixtureSource.get('empty');
  if (fixture.id === 'stopping' && query.get('sensor') === 'lost') {
    const lost = fixtureSource.get('sensor-lost');
    return { ...fixture, pressure: lost.pressure, graph: lost.graph };
  }
  return fixture;
}

function initialStep(fixture: Fixture): number {
  const value = new URLSearchParams(window.location.search).get('step');
  if (value !== null && /^\d+$/.test(value)) {
    const index = Number(value);
    if (Number.isSafeInteger(index) && index >= 0 && index < demoSteps.length) return index;
  }
  return Math.max(
    0,
    demoSteps.findIndex((item) => item.stage === fixture.stage),
  );
}

function RunScreen({
  fixture,
  parameters,
  onResult,
  onList,
  onFixture,
  step,
  onAdvance,
}: {
  fixture: Fixture;
  parameters: TestParameters;
  onResult: () => void;
  onList: () => void;
  onFixture: (id: FixtureId) => void;
  step: number;
  onAdvance: () => void;
}) {
  const item = demoSteps[step] ?? demoSteps[0];
  if (!item) return null;
  const stageIndex = fixtureSource.stages.findIndex((stage) => stage.id === item.stage);
  const isLost = fixture.pressure.quality !== 'valid';
  const isStopping = fixture.id === 'stopping';
  const isActuator = parameters.actuator;
  const { graph, pressureValue, lastPressure, differentialValue } = prepareRunDisplay(
    fixture,
    parameters,
    item.stage,
  );
  const instruction = isStopping
    ? 'Запрос отправлен. Завершение остановки и безопасное давление не подтверждены.'
    : isLost
      ? fixture.instruction
      : isActuator && item.stage === 'cycles'
        ? 'Пример подачи команд приводу: 10 подготовительных циклов. Положение клапана не подтверждается.'
        : isActuator && item.stage === 'valve'
          ? `Пример подачи команды на ${item.title === 'Открытие клапана' ? 'открытие' : 'закрытие'} привода; положение клапана не подтверждено.`
          : item.instruction;
  const action =
    isActuator && item.stage === 'cycles'
      ? 'Показать следующий этап'
      : isActuator && item.stage === 'valve'
        ? 'Показать подачу команды'
        : item.action;

  function advance() {
    if (step >= demoSteps.length - 1) {
      onResult();
      return;
    }
    onAdvance();
  }

  return (
    <>
      <div className="page-heading run-heading">
        <div>
          <button className="text-link" type="button" onClick={onList}>
            <ArrowLeft size={16} /> К испытаниям
          </button>
          <h1>Проведение испытания</h1>
          <p>
            {parameters.id} · {parameters.valve} ·{' '}
            {parameters.direction === 'pressure'
              ? 'Нагнетание · диафрагма №1'
              : 'Разрежение · диафрагма №2'}{' '}
            · {isActuator ? 'С приводом · положение неизвестно' : 'Без привода'}
          </p>
        </div>
      </div>
      <div className="run-grid">
        <aside className="run-stages" aria-label="Этапы испытания">
          <div className="section-heading">
            <h2>Этапы</h2>
          </div>
          <ol className="stage-list">
            {fixtureSource.stages.map((stage, index) => (
              <li
                key={stage.id}
                className={index === stageIndex ? 'stage current' : 'stage'}
                aria-current={index === stageIndex ? 'step' : undefined}
              >
                <span className="stage-marker">
                  {index < stageIndex ? <Check size={13} aria-hidden="true" /> : index + 1}
                </span>
                <span>
                  <strong>{stage.label}</strong>
                  <small className="sr-only">
                    {index === stageIndex
                      ? 'Текущий этап'
                      : index < stageIndex
                        ? 'Показан в макете'
                        : 'Ожидается в макете'}
                  </small>
                </span>
              </li>
            ))}
          </ol>
        </aside>
        <section className="run-content" aria-label="Измерения">
          <div className="measurement-top">
            <div className="section-heading">
              <div>
                <h2>Показания</h2>
              </div>
            </div>
            <div className="measurements">
              <div className="reading main-reading">
                <span>
                  Давление в ёмкости <small>ДД1</small>
                </span>
                <strong>
                  {pressureValue === null ? 'Неизвестно' : number(pressureValue)}{' '}
                  <small>{pressureValue === null ? '' : 'Па'}</small>
                </strong>
                {pressureValue === null && (
                  <p>
                    {isLost
                      ? lastPressure === null
                        ? 'Последнее показание недоступно'
                        : `Последнее: ${number(lastPressure)} Па · устарело, ${fixture.pressure.time}`
                      : 'Нет подготовленного показания'}
                  </p>
                )}
                {isStopping && isLost && <p>Данные ДД1 недоступны</p>}
              </div>
              <div className="reading target-reading">
                <span>
                  <PressureNotation /> производителя
                </span>
                <strong>
                  {number(parameters.pmax)} <small>Па</small>
                </strong>
              </div>
              <div className="reading differential-reading">
                <span>
                  Перепад на диафрагме <small>{fixture.differential.channel}</small>
                </span>
                <strong>
                  {differentialValue === null ? 'Неизвестно' : number(differentialValue)}{' '}
                  <small>{differentialValue === null ? '' : 'Па'}</small>
                </strong>
              </div>
            </div>
          </div>
          <div className="chart-panel">
            <div className="section-heading">
              <div>
                <h2>Изменение показаний</h2>
                <p>
                  {parameters.measurementSeconds === 60
                    ? 'Ряд и интервал измерения: 60 с'
                    : `Ряд: 60 с · интервал измерения: ${parameters.measurementSeconds} с`}
                </p>
              </div>
            </div>
            <MeasurementChart
              points={graph}
              target={parameters.pmax}
              tolerance={
                item.stage === 'stabilization'
                  ? 2
                  : item.stage === 'restabilization'
                    ? 5
                    : undefined
              }
            />
          </div>
        </section>
        <aside className="action-panel" aria-label="Текущее действие">
          <div>
            <h2>{isStopping ? 'Остановка' : isLost ? 'Потеря данных ДД1' : item.title}</h2>
            <p className="action-instruction">{instruction}</p>
            {!isLost && !isStopping && item.stage === 'measurement' && (
              <p className="action-instruction">
                Пример: прошло {number(parameters.measurementSeconds / 2)} с · осталось{' '}
                {number(parameters.measurementSeconds / 2)} с
              </p>
            )}
            {!isLost && !isStopping && isActuator && item.stage === 'cycles' && (
              <p className="action-instruction">Цикл 4 из 10 · пример</p>
            )}
            {!isLost && !isStopping && (
              <Button className="primary-button full-width" type="button" onClick={advance}>
                <span>{action}</span>
                <ArrowRight size={16} />
              </Button>
            )}
          </div>
          <div className="stop-area">
            <button
              className="stop-button full-width"
              type="button"
              onClick={() => onFixture('stopping')}
              disabled={isStopping}
            >
              <Pause size={16} />
              {isStopping ? 'Остановка выполняется' : 'Остановить испытание'}
            </button>
          </div>
        </aside>
      </div>
    </>
  );
}

export function App() {
  const [initial] = useState(initialFixture);
  const [fixture, setFixture] = useState(initial);
  const [parameters, setParameters] = useState<TestParameters>(initial.parameters);
  const [draftBase, setDraftBase] = useState(initial.parameters);
  const [draft, setDraft] = useState(() => draftFrom(initial.parameters));
  const [draftAvailable, setDraftAvailable] = useState(
    initial.screen === 'readiness' || initial.screen === 'parameters',
  );
  const [screen, setScreen] = useState<Screen>(initial.screen);
  const [active, setActive] = useState(initial.screen === 'run');
  const [step, setStep] = useState(() => initialStep(initial));
  const [nextId, setNextId] = useState(2);
  const mainRef = useRef<HTMLElement>(null);
  const activeRun = useRef<{ fixture: Fixture; parameters: TestParameters; step: number } | null>(
    initial.screen === 'run'
      ? { fixture: initial, parameters: initial.parameters, step: initialStep(initial) }
      : null,
  );

  useEffect(() => {
    document.title = `${headings[screen]} — ГК80`;
    mainRef.current?.focus();
  }, [screen]);

  function selectFixture(id: FixtureId, preserveParameters = false) {
    const base = fixtureSource.get(id);
    const selected =
      preserveParameters && id === 'stopping'
        ? {
            ...base,
            pressure: fixture.pressure,
            differential: fixture.differential,
            graph: fixture.graph,
          }
        : base;
    setFixture(selected);
    if (!preserveParameters) {
      setParameters(selected.parameters);
      setDraftBase(selected.parameters);
      setDraft(draftFrom(selected.parameters));
      setDraftAvailable(selected.screen === 'readiness' || selected.screen === 'parameters');
    }
    setScreen(selected.screen);
    setActive(selected.screen === 'run');
    const selectedStep =
      preserveParameters && id === 'stopping'
        ? step
        : Math.max(
            0,
            demoSteps.findIndex((item) => item.stage === selected.stage),
          );
    setStep(selectedStep);
    activeRun.current =
      selected.screen === 'run'
        ? {
            fixture: selected,
            parameters: preserveParameters ? parameters : selected.parameters,
            step: selectedStep,
          }
        : null;
    const url = new URL(window.location.href);
    url.searchParams.set('fixture', id);
    if (preserveParameters && id === 'stopping') {
      url.searchParams.set('step', String(selectedStep));
    } else {
      url.searchParams.delete('step');
    }
    if (id === 'stopping' && selected.pressure.quality !== 'valid') {
      url.searchParams.set('sensor', 'lost');
    } else {
      url.searchParams.delete('sensor');
    }
    window.history.replaceState(null, '', url);
  }
  function beginNew() {
    const newParameters = {
      ...fixtureSource.initial,
      id: `ДЕМО-${String(nextId).padStart(3, '0')}`,
      valve: '',
    };
    setParameters(newParameters);
    setDraftBase(newParameters);
    setDraft(draftFrom(newParameters));
    setDraftAvailable(true);
    setNextId((id) => id + 1);
    setFixture(fixtureSource.get('empty'));
    setActive(false);
    activeRun.current = null;
    setScreen('parameters');
  }
  function startRun() {
    setFixture({ ...fixtureSource.get('waiting'), stage: 'cycles' });
    setStep(0);
    setActive(true);
    setDraftAvailable(false);
    activeRun.current = {
      fixture: { ...fixtureSource.get('waiting'), stage: 'cycles' },
      parameters,
      step: 0,
    };
    const url = new URL(window.location.href);
    url.searchParams.set('fixture', 'waiting');
    url.searchParams.set('step', '0');
    window.history.replaceState(null, '', url);
    setScreen('run');
  }
  function openList() {
    if (screen === 'run') activeRun.current = { fixture, parameters, step };
    setScreen('tests');
  }
  function openParameters() {
    if (screen === 'run') activeRun.current = { fixture, parameters, step };
    setScreen('parameters');
  }
  function openRun() {
    if (screen === 'run') return;
    if (activeRun.current) {
      setFixture(activeRun.current.fixture);
      setParameters(activeRun.current.parameters);
      setStep(activeRun.current.step);
      setScreen('run');
    }
  }
  const activeFixture = screen === 'run' ? fixture : activeRun.current?.fixture;
  const activeNotice =
    activeFixture?.id === 'stopping'
      ? activeFixture.pressure.quality !== 'valid'
        ? 'Пример: потеря ДД1 · остановка не подтверждена'
        : 'Пример: остановка не подтверждена'
      : activeFixture && activeFixture.pressure.quality !== 'valid'
        ? 'Пример: потеря ДД1'
        : null;
  return (
    <div className="app-shell">
      <div className="workspace">
        <nav className="sidebar" aria-label="Навигация">
          <div className="brand">
            <div className="brand-symbol">
              <Activity size={20} aria-hidden="true" />
            </div>
            <div>
              <strong>ГК80</strong>
              <span>Испытания клапанов</span>
            </div>
          </div>
          <button
            type="button"
            className={screen === 'tests' ? 'nav-item active' : 'nav-item'}
            aria-current={screen === 'tests' ? 'page' : undefined}
            onClick={openList}
          >
            <ListChecks size={18} /> Испытания
          </button>
          {active && (
            <button
              type="button"
              className={screen === 'run' ? 'nav-item active' : 'nav-item'}
              aria-current={screen === 'run' ? 'page' : undefined}
              onClick={openRun}
            >
              <Activity size={18} aria-hidden="true" /> <span>Текущее испытание</span>
            </button>
          )}
          {(active || draftAvailable) && (
            <button
              type="button"
              className={screen === 'parameters' ? 'nav-item active' : 'nav-item'}
              aria-current={screen === 'parameters' ? 'page' : undefined}
              onClick={openParameters}
            >
              <FileText size={18} aria-hidden="true" /> <span>Параметры</span>
            </button>
          )}
          {active && screen !== 'run' && activeNotice && (
            <span className="nav-status" role="status">
              {activeNotice}
            </span>
          )}
        </nav>
        <main className="main-content" ref={mainRef} tabIndex={-1} aria-label={headings[screen]}>
          {screen === 'tests' && (
            <TestsScreen
              onNew={draftAvailable ? openParameters : beginNew}
              draftAvailable={draftAvailable}
              onOpen={openRun}
              onHistory={() => {
                setFixture(fixtureSource.get('measuring'));
                setParameters(fixtureSource.initial);
                setScreen('result');
              }}
              empty={fixture.id === 'empty'}
              activeParameters={active ? (activeRun.current?.parameters ?? parameters) : null}
            />
          )}
          {screen === 'parameters' &&
            (active && activeRun.current ? (
              <ActiveParametersScreen
                parameters={activeRun.current.parameters}
                onReturn={openRun}
              />
            ) : (
              <ParametersScreen
                initial={draftBase}
                draft={draft}
                setDraft={setDraft}
                onBack={() => setScreen('tests')}
                onContinue={(value) => {
                  setParameters(value);
                  setScreen('readiness');
                }}
              />
            ))}
          {screen === 'readiness' && (
            <ReadinessScreen
              parameters={parameters}
              onBack={() => setScreen('parameters')}
              onStart={startRun}
            />
          )}
          {screen === 'run' && (
            <RunScreen
              fixture={fixture}
              parameters={parameters}
              step={step}
              onAdvance={() => {
                const next = step + 1;
                setStep(next);
                const url = new URL(window.location.href);
                url.searchParams.set('fixture', fixture.id);
                url.searchParams.set('step', String(next));
                window.history.replaceState(null, '', url);
              }}
              onResult={() => {
                setActive(false);
                setDraftAvailable(false);
                activeRun.current = null;
                setScreen('result');
              }}
              onList={openList}
              onFixture={(id) => selectFixture(id, true)}
            />
          )}
          {screen === 'result' && (
            <ResultScreen
              parameters={parameters}
              saveError={fixture.id === 'save-error'}
              interrupted={fixture.id === 'stopping'}
              active={active}
              draftAvailable={draftAvailable}
              onList={() => setScreen('tests')}
              onNew={() => {
                if (draftAvailable) {
                  openParameters();
                  return;
                }
                const newParameters = {
                  ...parameters,
                  id: `ДЕМО-${String(nextId).padStart(3, '0')}`,
                };
                setParameters(newParameters);
                setDraftBase(newParameters);
                setDraft(draftFrom(newParameters));
                setDraftAvailable(true);
                setNextId((id) => id + 1);
                setScreen('parameters');
              }}
            />
          )}
        </main>
      </div>
      <div className="fixture-toolbar">
        <div>
          <span>Состояние макета</span>
        </div>
        <label>
          <span className="sr-only">Выбрать подготовленное состояние</span>
          <select
            value={fixture.id}
            onChange={(event) => {
              const id = fixtureSource.ids.find((candidate) => candidate === event.target.value);
              if (id) selectFixture(id);
            }}
          >
            {fixtureSource.ids.map((id) => (
              <option key={id} value={id}>
                {fixtureSource.get(id).label}
              </option>
            ))}
          </select>
        </label>
        <span className="demo-badge">
          <span className="demo-dot" /> Макет · подготовленные данные
        </span>
      </div>
    </div>
  );
}
