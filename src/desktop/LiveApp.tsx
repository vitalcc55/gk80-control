import {
  type Dispatch,
  type SetStateAction,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react';
import {
  ActiveParametersScreen,
  draftFrom,
  ParametersScreen,
  ReadinessScreen,
  ResultScreen,
} from '../components/OtherScreens';
import type {
  CommandStatus,
  Readiness,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { TestParameters as FormParameters } from '../model';
import type { SessionBridge } from './bridge';
import { type Pending, pendingKey, terminalStatus } from './command-attempt-state';
import { type CommandCoordinator, createCommandCoordinator } from './command-coordinator';
import type { DemoService } from './demo-service';
import { History } from './History';
import { LiveRun } from './LiveRun';
import { observeReadiness } from './readiness-observation';
import { SessionStatus } from './SessionStatus';
import { type SessionObservation, sessionObservation } from './session-observation';
import { useSessionObservation } from './use-session-observation';

const empty: FormParameters = {
  id: '',
  valve: '',
  area: 0.012,
  pmax: 10000,
  direction: 'pressure',
  actuator: false,
  voltage: 24,
  openSeconds: 3,
  closeSeconds: 3,
  measurementSeconds: 60,
};
function newDraft() {
  return { ...draftFrom(empty), area: '', pmax: '' };
}
const legacyCommandLabels: Record<Pending['kind'], string> = {
  create: 'создание испытания',
  start: 'запуск испытания',
  confirm: 'подтверждение действия',
  stop: 'остановка испытания',
};

function commandStatusLabel(status: CommandStatus['status'] | undefined): string {
  switch (status) {
    case 'accepted':
      return 'принята; завершение команды не подтверждено';
    case 'completed':
      return 'выполнена';
    case 'rejected':
      return 'отклонена';
    case 'failed':
      return 'завершилась с ошибкой';
    default:
      return 'исход неизвестен';
  }
}

type LiveAppProps = {
  session: SessionBridge;
  service: DemoService;
  hostRunId: string;
  backendRunId: string;
  databaseId: string;
  diagnosticsDegraded?: boolean;
  pendingStorage?: Storage;
  suspended?: boolean;
};

const observationDatabases = new WeakMap<
  SessionObservation,
  { database: string; minimum: number }
>();

function bindObservationDatabase(props: LiveAppProps) {
  const observer = sessionObservation(props.hostRunId, props.backendRunId);
  const previous = observationDatabases.get(observer);
  const minimum =
    previous?.database === props.databaseId
      ? previous.minimum
      : previous
        ? observer.getSnapshot().observationVersion + 1
        : 0;
  observationDatabases.set(observer, { database: props.databaseId, minimum });
  return {
    host: props.hostRunId,
    backend: props.backendRunId,
    database: props.databaseId,
    minimum,
  };
}

export function LiveApp(props: LiveAppProps) {
  const [binding, setBinding] = useState(() => bindObservationDatabase(props));
  let currentBinding = binding;
  if (
    binding.host !== props.hostRunId ||
    binding.backend !== props.backendRunId ||
    binding.database !== props.databaseId
  ) {
    currentBinding = bindObservationDatabase(props);
    setBinding(currentBinding);
  }
  const [draft, setDraft] = useState(newDraft);
  const [draftAvailable, setDraftAvailable] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  const { hostRunId, backendRunId, databaseId, pendingStorage } = props;
  const coordinator = useMemo(
    () =>
      createCommandCoordinator({
        hostRunId,
        backendRunId,
        databaseId,
        ...(pendingStorage ? { pendingStorage } : {}),
      }),
    [hostRunId, backendRunId, databaseId, pendingStorage],
  );
  const createdVersion = useSyncExternalStore(
    coordinator.subscribe,
    () => coordinator.getSnapshot().createdVersion,
  );
  const createdAcknowledgement = useRef({ coordinator, version: 0 });
  useLayoutEffect(() => {
    if (createdAcknowledgement.current.coordinator !== coordinator)
      createdAcknowledgement.current = { coordinator, version: 0 };
    if (createdVersion <= createdAcknowledgement.current.version) return;
    createdAcknowledgement.current.version = createdVersion;
    setShowForm(false);
    setDraft(newDraft());
    setDraftAvailable(false);
  }, [coordinator, createdVersion]);
  if (props.suspended) return null;
  return (
    <ActiveLiveApp
      key={`${props.hostRunId}:${props.backendRunId}:${props.databaseId}`}
      {...props}
      coordinator={coordinator}
      minimumObservation={currentBinding.minimum}
      draft={draft}
      setDraft={setDraft}
      draftAvailable={draftAvailable}
      setDraftAvailable={setDraftAvailable}
      showForm={showForm}
      setShowForm={setShowForm}
      showHistory={showHistory}
      setShowHistory={setShowHistory}
    />
  );
}

function ActiveLiveApp({
  session,
  service,
  hostRunId,
  backendRunId,
  diagnosticsDegraded = false,
  minimumObservation,
  coordinator,
  draft,
  setDraft,
  draftAvailable,
  setDraftAvailable,
  showForm,
  setShowForm,
  showHistory,
  setShowHistory,
}: LiveAppProps & {
  minimumObservation: number;
  coordinator: CommandCoordinator;
  draft: ReturnType<typeof newDraft>;
  setDraft: Dispatch<SetStateAction<ReturnType<typeof newDraft>>>;
  draftAvailable: boolean;
  setDraftAvailable: Dispatch<SetStateAction<boolean>>;
  showForm: boolean;
  setShowForm: Dispatch<SetStateAction<boolean>>;
  showHistory: boolean;
  setShowHistory: Dispatch<SetStateAction<boolean>>;
}) {
  const { observer, projection } = useSessionObservation(session, hostRunId, backendRunId);
  const acceptedProjection = projection.observationVersion >= minimumObservation;
  const connection = acceptedProjection ? projection.connection : null;
  const lastSnapshot = acceptedProjection ? projection.lastSnapshot : null;
  const { gapVersion } = projection;
  const [showSavedParameters, setShowSavedParameters] = useState(false);
  const [readiness, setReadiness] = useState<Readiness | null>(null);
  const [readinessConfirmed, setReadinessConfirmed] = useState(false);
  const input = useMemo(
    () => ({
      service,
      connection,
      lastSnapshot,
      isCurrentConnection: (value: typeof connection) =>
        observer.getSnapshotFor(session).connection === value,
    }),
    [service, connection, lastSnapshot, observer, session],
  );
  useLayoutEffect(() => {
    coordinator.update(input);
  }, [coordinator, input]);
  useLayoutEffect(() => {
    coordinator.start();
    return () => coordinator.dispose();
  }, [coordinator]);
  const state = useSyncExternalStore(coordinator.subscribe, coordinator.getSnapshot);
  const {
    pending,
    legacy,
    legacyBlocks,
    storageUnconfirmed,
    message,
    commandFailure,
    latestNotice,
    storageMessage,
    commandMessages,
    stopMessage,
    volatileStopTracking,
    stopRequestedFor,
    stopRequested,
    stopCount,
    totalStops,
    blocking,
  } = state;
  const { stop, lookup, forget, showMessage } = coordinator;
  const snapshot = connection?.view.snapshot ?? lastSnapshot;
  const record = snapshot?.activeTest ?? null;
  const activeId = record?.testId;
  const stage = record?.stage;
  const revision = snapshot?.revision;
  const fresh =
    !!connection &&
    !!snapshot &&
    Number.isSafeInteger(snapshot.revision) &&
    Number.isSafeInteger(snapshot.lastEventSequence);
  const commandMessageRegion = useRef<HTMLDivElement>(null);
  const stopMessageRegion = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if ((latestNotice === 'message' ? message : commandFailure) && commandMessageRegion.current)
      commandMessageRegion.current.scrollTop = 0;
  }, [message, commandFailure, latestNotice]);
  useLayoutEffect(() => {
    if (stopMessage && stopMessageRegion.current) stopMessageRegion.current.scrollTop = 0;
  }, [stopMessage]);
  useEffect(() => {
    setReadinessConfirmed(false);
    if (fresh && stage === 'preparing' && activeId && revision !== undefined) {
      return observeReadiness(service, hostRunId, backendRunId, activeId, revision, (value) => {
        if (value) {
          setReadiness(value);
          setReadinessConfirmed(true);
        } else {
          setReadinessConfirmed(false);
          showMessage('Не удалось подтвердить готовность.');
        }
      });
    }
    return undefined;
  }, [fresh, activeId, stage, revision, service, hostRunId, backendRunId, showMessage]);

  const formParameters = record
    ? {
        ...empty,
        id: record.testId,
        valve: record.parameters.valve,
        area: record.parameters.areaM2,
        pmax: record.parameters.pmaxPa,
        measurementSeconds: record.parameters.measurementSeconds,
      }
    : empty;
  return (
    <div className="app-shell">
      <div className="workspace">
        <nav className="sidebar" aria-label="Навигация">
          <div className="brand">
            <strong>ГК80</strong>
            <span>Испытания клапанов</span>
          </div>
          <button type="button" className="nav-item" onClick={() => setShowHistory(true)}>
            Испытания
          </button>
          {record && record.stage !== 'finished' && (
            <button type="button" className="nav-item" onClick={() => setShowHistory(false)}>
              Текущее испытание
            </button>
          )}
        </nav>
        <main className="main-content">
          <p>Демонстрация · тестовые данные</p>
          <SessionStatus
            bridge={session}
            hostRunId={hostRunId}
            backendRunId={backendRunId}
            snapshotAccepted={fresh}
          />
          <p role="status" aria-live="polite" className="live-diagnostics-warning">
            {diagnosticsDegraded ? 'Журнал диагностики недоступен или неполон.' : ''}
          </p>
          <section className="demo-messages" aria-label="Сообщения команд">
            <div className="demo-message-primary">
              <div
                className="demo-stop-message"
                ref={stopMessageRegion}
                tabIndex={stopMessage ? 0 : -1}
              >
                {stopMessage && (
                  <p role="status">
                    {stopRequestedFor && stopRequestedFor !== activeId
                      ? `Испытание ${stopRequestedFor}: `
                      : ''}
                    {stopMessage}
                  </p>
                )}
              </div>
              <div
                className="demo-command-message"
                ref={commandMessageRegion}
                tabIndex={
                  message || commandFailure || storageMessage || commandMessages.size ? 0 : -1
                }
              >
                {storageMessage && <p role="alert">{storageMessage}</p>}
                {message && latestNotice === 'message' && <p role="status">{message}</p>}
                {commandFailure && (
                  <p role="status">
                    {commandFailure.text} (команда {commandFailure.id}
                    {commandFailure.testId && `; испытание ${commandFailure.testId}`})
                  </p>
                )}
                {message && latestNotice !== 'message' && <p role="status">{message}</p>}
                {[...commandMessages].map(([key, { id, text }]) => (
                  <p role="status" key={key}>
                    {commandMessages.size > 1
                      ? `${key === id ? 'Команда' : 'Остановка'} ${id}: `
                      : ''}
                    {text}
                  </p>
                ))}
              </div>
            </div>
            <div
              className="demo-message-list"
              tabIndex={
                legacy.unreadable ||
                legacy.items.length ||
                pending.length ||
                storageUnconfirmed ||
                totalStops >= 63 ||
                stopCount >= 2 ||
                volatileStopTracking
                  ? 0
                  : -1
              }
            >
              {(legacy.unreadable || legacy.items.length > 0) && (
                <p role="alert">
                  {legacy.unreadable
                    ? 'Не удалось прочитать прежний список команд; их возможный исход не подтверждён.'
                    : 'Есть прежние команды с неизвестной принадлежностью к базе; их исход не подтверждён.'}
                  {legacyBlocks &&
                    ' Новые команды недоступны до выяснения их исхода; остановка доступна.'}
                </p>
              )}
              {legacy.unreadable && (
                <button
                  type="button"
                  onClick={() => {
                    coordinator.readLegacy();
                  }}
                >
                  Повторить чтение прежних команд
                </button>
              )}
              {legacy.items.map((item) => (
                <p key={item.id}>
                  Прежняя команда: {legacyCommandLabels[item.kind]}, ID {item.id}
                  {item.testId && `; испытание ${item.testId}`}.
                  {item.kind === 'stop' && ' Безопасное давление не подтверждено.'}
                </p>
              ))}
              {!legacy.unreadable && legacyBlocks && (
                <p>
                  Подтверждение не устанавливает исход команд и не связывает их с текущей базой.{' '}
                  <button type="button" onClick={() => coordinator.reviewLegacy()}>
                    Я проверил прежние команды
                  </button>
                </p>
              )}
              {totalStops >= 63 && (
                <p role="alert">
                  Достигнут предел неподтверждённых запросов остановки. Закройте приложение и
                  запустите его снова перед новым испытанием.
                </p>
              )}
              {storageUnconfirmed && (
                <p role="alert">
                  Не удалось восстановить список команд. Новые команды недоступны.{' '}
                  <button
                    type="button"
                    onClick={() => {
                      coordinator.retryStorage();
                    }}
                  >
                    Повторить чтение команд
                  </button>
                </p>
              )}
              {volatileStopTracking && (
                <p role="alert">
                  Исход остановки отслеживается только в памяти либо его сохранение не подтверждено:
                  после перезагрузки или закрытия окна потребуется проверить историю испытания.
                </p>
              )}
              {pending.length >= 64 && (
                <p role="alert">
                  Достигнут предел отслеживаемых команд. Проверьте завершённые испытания и скройте
                  ненужные записи наблюдения.
                </p>
              )}
              {pending.map((item) => (
                <p key={pendingKey(item)}>
                  Команда: {commandStatusLabel(item.status?.status)}. ID {item.id}.{' '}
                  {item.kind === 'stop' && `Остановка испытания ${item.testId ?? 'неизвестно'}. `}
                  {item.kind === 'stop' && 'Безопасное давление не подтверждено. '}
                  {item.legacyUnverifiable &&
                    'Исходное содержимое команды не сохранилось; её исход нельзя установить по идентификатору. Подтверждение проверки не устанавливает исход. '}
                  <button
                    type="button"
                    onClick={() => {
                      void lookup(item, true);
                    }}
                  >
                    Проверить состояние
                  </button>
                  {item.legacyUnverifiable && !item.retired && (
                    <button
                      type="button"
                      onClick={() => {
                        coordinator.reviewAttempt(item);
                      }}
                    >
                      Я проверил эту команду
                    </button>
                  )}
                  {item.retired && (
                    <button
                      type="button"
                      disabled={!coordinator.canForget(item)}
                      onClick={() => forget(item)}
                    >
                      {item.legacyUnverifiable
                        ? 'Удалить запись наблюдения'
                        : !terminalStatus(item.status)
                          ? 'Прекратить наблюдение'
                          : 'Скрыть наблюдение команды'}
                    </button>
                  )}
                  {item.retired &&
                    item.kind === 'stop' &&
                    !terminalStatus(item.status) &&
                    ' После удаления автоматическая сверка этой попытки невозможна.'}
                </p>
              ))}
              {stopCount >= 2 && (
                <p role="alert">
                  Ответы на запросы остановки не подтверждены. Закройте приложение и запустите его
                  снова; безопасное давление не подтверждено.
                </p>
              )}
            </div>
          </section>
          {(showHistory || (!record && !showForm)) && (
            <History
              service={service}
              available={fresh}
              disabled={!fresh || blocking || (!!record && record.stage !== 'finished')}
              onNew={() => {
                setShowHistory(false);
                if (!draftAvailable) {
                  setDraft(newDraft());
                  setDraftAvailable(true);
                }
                setShowForm(true);
              }}
              onCopy={(value) => {
                setDraft(
                  draftFrom({
                    ...empty,
                    valve: value.valve,
                    area: value.areaM2,
                    pmax: value.pmaxPa,
                    measurementSeconds: value.measurementSeconds,
                  }),
                );
                setDraftAvailable(true);
                setShowHistory(false);
                setShowForm(true);
              }}
              draftAvailable={draftAvailable}
              onDiscard={() => {
                setDraft(newDraft());
                setDraftAvailable(false);
              }}
            />
          )}
          {!showHistory && (!record || record.stage === 'finished') && showForm && (
            <ParametersScreen
              initial={empty}
              draft={draft}
              setDraft={setDraft}
              live
              pending={pending.some((item) => item.kind === 'create' && !item.retired)}
              unavailable={!fresh || blocking}
              onBack={() => {
                setShowForm(false);
                setShowHistory(true);
              }}
              onContinue={(parameters) => {
                void coordinator.create(parameters);
              }}
            />
          )}
          {!showHistory && showSavedParameters && record?.stage === 'preparing' && (
            <ActiveParametersScreen
              live
              parameters={formParameters}
              onReturn={() => setShowSavedParameters(false)}
            />
          )}
          {!showHistory &&
            !showSavedParameters &&
            record?.stage === 'preparing' &&
            readiness?.testId === record.testId && (
              <ReadinessScreen
                key={`${record.testId}:${readiness.revision}`}
                parameters={formParameters}
                autoCheckFailed={
                  !fresh ||
                  !readinessConfirmed ||
                  !readiness.ready ||
                  readiness.revision !== snapshot?.revision
                }
                readiness={readiness}
                pending={blocking}
                stale={!fresh || !readinessConfirmed || readiness.revision !== snapshot?.revision}
                onBack={() => setShowSavedParameters(true)}
                onStart={(checks) => {
                  void coordinator.startTest(checks);
                }}
              />
            )}
          {!showHistory &&
            !showSavedParameters &&
            record?.stage === 'preparing' &&
            readiness?.testId !== record.testId && <p>Готовность ещё не подтверждена.</p>}
          {record && record.stage !== 'finished' && (
            <button
              className="work-button work-button-secondary"
              type="button"
              disabled={stopCount >= 2 || totalStops >= 64}
              onClick={() => {
                void stop();
              }}
            >
              {stopRequested ? 'Повторить запрос остановки' : 'Остановить испытание'}
            </button>
          )}
          {!showHistory && record?.stage === 'running' && snapshot && (
            <LiveRun
              key={record.testId}
              record={record}
              snapshot={snapshot}
              fresh={fresh}
              gapVersion={gapVersion}
              pending={blocking}
              onConfirm={() => {
                void coordinator.confirmAction();
              }}
            />
          )}
          {!showHistory && record?.stage === 'stopping' && (
            <p>Остановка выполняется; безопасное давление не подтверждено.</p>
          )}
          {!showHistory && !showForm && record?.stage === 'finished' && (
            <ResultScreen
              parameters={formParameters}
              record={record}
              saveError={record.saveStatus === 'failed'}
              interrupted={record.outcome === 'interrupted'}
              active={blocking}
              draftAvailable={draftAvailable}
              onList={() => setShowHistory(true)}
              onNew={() => {
                if (!draftAvailable) {
                  setDraft(draftFrom(formParameters));
                  setDraftAvailable(true);
                }
                setShowForm(true);
              }}
            />
          )}
        </main>
      </div>
    </div>
  );
}
