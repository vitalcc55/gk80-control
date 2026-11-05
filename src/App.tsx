import { ArrowRight } from 'lucide-react';
import { useRef, useState } from 'react';
import { ActiveTestPanel } from './components/ActiveTestPanel';
import { AppShell } from './components/AppShell';
import { ActiveParametersScreen, draftFrom, ParametersScreen } from './components/ParametersScreen';
import { ReadinessScreen } from './components/ReadinessScreen';
import { ResultScreen } from './components/ResultScreen';
import { RunScreen as SharedRunScreen } from './components/RunScreen';
import { StopButton } from './components/StopButton';
import { Button } from './components/ui/button';
import { useScreenFocus } from './components/use-screen-focus';
import { FixtureTests } from './FixtureTests';
import { demoSteps, fixtureSource, prepareRunDisplay } from './fixtureAdapter';
import { formatElapsedSeconds, formatNumber, formatRemainingSeconds } from './lib/format';
import type { Fixture, FixtureId, Screen, TestParameters } from './model';

const number = (value: number) => formatNumber(value, 12);

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
  const actionHeadingRef = useRef<HTMLHeadingElement>(null);
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
    <SharedRunScreen
      onList={onList}
      subtitle={
        <>
          {parameters.id} · {parameters.valve} ·{' '}
          {parameters.direction === 'pressure'
            ? 'Нагнетание · диафрагма №1'
            : 'Разрежение · диафрагма №2'}{' '}
          · {isActuator ? 'С приводом · положение неизвестно' : 'Без привода'}
        </>
      }
      stages={fixtureSource.stages.map((stage, index) => ({
        ...stage,
        state: index === stageIndex ? 'current' : index < stageIndex ? 'done' : 'next',
      }))}
      pressure={pressureValue === null ? 'Неизвестно' : `${number(pressureValue)} Па`}
      differential={differentialValue === null ? 'Неизвестно' : `${number(differentialValue)} Па`}
      target={`${formatNumber(parameters.pmax)} Па`}
      pressureNote={
        <>
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
        </>
      }
      chart={{
        points: graph,
        target: parameters.pmax,
        rangeSeconds: item.stage === 'measurement' ? parameters.measurementSeconds : undefined,
        tolerance:
          item.stage === 'stabilization' ? 2 : item.stage === 'restabilization' ? 5 : undefined,
      }}
      chartNote={
        item.stage === 'measurement'
          ? `Ряд: ${number(parameters.measurementSeconds / 2)} с · период усреднения утечки: ${number(parameters.measurementSeconds)} с`
          : 'Ряд: 60 с'
      }
      headingRef={actionHeadingRef}
      actionTitle={isStopping ? 'Остановка' : isLost ? 'Потеря данных ДД1' : item.title}
      instruction={instruction}
      time={
        !isLost &&
        !isStopping && (
          <>
            {item.stage === 'measurement' && (
              <p className="action-instruction">
                Пример: прошло {formatElapsedSeconds(parameters.measurementSeconds / 2)} с ·
                осталось {formatRemainingSeconds(parameters.measurementSeconds / 2)} с
              </p>
            )}
            {isActuator && item.stage === 'cycles' && (
              <p className="action-instruction">Цикл 4 из 10 · пример</p>
            )}
          </>
        )
      }
      action={
        !isLost &&
        !isStopping && (
          <Button className="primary-button full-width" type="button" onClick={advance}>
            <span>{action}</span>
            <ArrowRight size={16} />
          </Button>
        )
      }
      stop={
        <StopButton
          disabled={isStopping}
          label={isStopping ? 'Остановка выполняется' : 'Остановить испытание'}
          onStop={() => {
            onFixture('stopping');
            actionHeadingRef.current?.focus();
          }}
        />
      }
    />
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
  const [screen, setScreenValue] = useState<Screen>(initial.screen);
  const [historyResult, setHistoryResult] = useState(false);
  const { mainRef, requestFocus } = useScreenFocus(screen);
  const setScreen = (destination: Screen) => {
    setHistoryResult(false);
    requestFocus(destination);
    setScreenValue(destination);
  };
  const [active, setActive] = useState(initial.screen === 'run');
  const [step, setStep] = useState(() => initialStep(initial));
  const [nextId, setNextId] = useState(2);
  const [history, setHistory] = useState<readonly TestParameters[]>([]);
  const [emptyHistory, setEmptyHistory] = useState(initial.id === 'empty');
  const [showExample, setShowExample] = useState(
    initial.id !== 'empty' && initial.id !== 'save-error',
  );
  const activeRun = useRef<{ fixture: Fixture; parameters: TestParameters; step: number } | null>(
    initial.screen === 'run'
      ? { fixture: initial, parameters: initial.parameters, step: initialStep(initial) }
      : null,
  );

  function selectFixture(id: FixtureId, preserveParameters = false) {
    const base = fixtureSource.get(id);
    const context = preserveParameters
      ? screen === 'run'
        ? { fixture, parameters, step }
        : activeRun.current
      : null;
    const selected =
      context && id === 'stopping'
        ? {
            ...base,
            pressure: context.fixture.pressure,
            differential: context.fixture.differential,
            graph: context.fixture.graph,
          }
        : base;
    setFixture(selected);
    if (context) setParameters(context.parameters);
    if (!preserveParameters) {
      if (id === 'empty') setHistory([]);
      setEmptyHistory(id === 'empty' || id === 'save-error');
      setShowExample(id !== 'empty' && id !== 'save-error');
      setParameters(selected.parameters);
      setDraftBase(selected.parameters);
      setDraft(draftFrom(selected.parameters));
      setDraftAvailable(selected.screen === 'readiness' || selected.screen === 'parameters');
    }
    setScreen(selected.screen);
    setActive(selected.screen === 'run');
    const selectedStep =
      context && id === 'stopping'
        ? context.step
        : Math.max(
            0,
            demoSteps.findIndex((item) => item.stage === selected.stage),
          );
    setStep(selectedStep);
    activeRun.current =
      selected.screen === 'run'
        ? {
            fixture: selected,
            parameters: context?.parameters ?? selected.parameters,
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
    <AppShell
      screen={screen}
      historyResult={historyResult}
      active={active}
      draftAvailable={draftAvailable}
      announcement={
        screen === 'run' ? (activeNotice ?? `Этап: ${demoSteps[step]?.title ?? ''}`) : null
      }
      onTests={openList}
      onRun={openRun}
      onParameters={openParameters}
      onResult={
        !active && history.length
          ? () => {
              const last = history[0];
              if (last) {
                setParameters(last);
                setFixture(fixtureSource.get('measuring'));
                setScreen('result');
              }
            }
          : undefined
      }
      mainRef={mainRef}
      notice={active && screen !== 'run' ? activeNotice : null}
      footer={
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
      }
    >
      <div className={active && screen !== 'run' ? 'active-route-layout' : undefined}>
        <div className="route-content">
          {screen === 'tests' && (
            <FixtureTests
              onNew={draftAvailable ? openParameters : beginNew}
              draftAvailable={draftAvailable}
              onHistory={(selected) => {
                setFixture(fixtureSource.get('measuring'));
                setParameters(selected);
                setScreen('result');
                setHistoryResult(true);
              }}
              empty={emptyHistory}
              showExample={showExample}
              history={history}
              example={fixtureSource.initial}
              activeParameters={active ? (activeRun.current?.parameters ?? parameters) : null}
            />
          )}
          {screen === 'parameters' &&
            (active && activeRun.current ? (
              <ActiveParametersScreen parameters={activeRun.current.parameters} />
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
              autoCheckFailed={fixture.id === 'readiness-failed'}
              onBack={() => setScreen('parameters')}
              onStart={startRun}
            />
          )}
          {screen === 'run' && (
            <RunScreen
              onList={openList}
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
                setEmptyHistory(false);
                setHistory((current) => [
                  parameters,
                  ...current.filter((item) => item.id !== parameters.id),
                ]);
                setActive(false);
                setDraftAvailable(false);
                activeRun.current = null;
                setScreen('result');
              }}
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
        </div>
        {active && screen !== 'run' && (
          <ActiveTestPanel
            title={`${(activeRun.current?.parameters ?? parameters).id} · ${(activeRun.current?.parameters ?? parameters).valve}`}
            status="Пример активного прохода"
            description={activeNotice ?? 'Подготовленные данные · испытание в макете'}
            onReturn={openRun}
            stop={
              <StopButton
                disabled={activeFixture?.id === 'stopping'}
                label={
                  activeFixture?.id === 'stopping'
                    ? 'Остановка выполняется'
                    : 'Остановить испытание'
                }
                onStop={() => {
                  selectFixture('stopping', true);
                }}
              />
            }
          />
        )}
      </div>
    </AppShell>
  );
}
