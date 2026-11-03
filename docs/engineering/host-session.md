# Внутренние обязанности host-сессии

Основание: [#17](https://github.com/vitalcc55/gk80-control/issues/17), разделы 1–3 [аудита #11](https://github.com/vitalcc55/gk80-control/issues/11). База — `c1916f1de0f5ea286f3038a637f4fcb1fda48c90`, после принятых #12–#16. Это перенос обязанностей без изменения публичной семантики, временной политики и лимитов.

## Контракт результата

Один `demoSession` координирует подтверждения связи, поток и знания host о командах. Один `Owner` владеет своим дочерним процессом и временем жизни доставки. Поток не разбирает идентичность команд; ledger не управляет сокетом и не определяет свежесть. Shutdown связывает их результаты, сохраняя раннюю Stop и различая исход команды, сохранение, очистку потока и выход процесса.

Доказательство — существующие предметные регрессии lease/recovery, независимой Stop, exact lookup, поздней истории, bounded delivery, startup/Verify и shutdown, затем `go test -count=1 ./internal/desktop` и `go test -count=1 ./internal/transport ./internal/browser`, применимые vet/lint/race и отдельные реальные Windows/IAB demo-проходы. Тесты, сборки, штатные проходы и диагностические подмены подтверждают разные свойства.

Не входят frontend-координаторы #15/#16, core admission/Engine/Store, SQLite, протокол, RPC, identity, сроки и лимиты, #18–#22, оборудование и новая инфраструктура. Блокер полного переноса — доказанная необходимость изменить принятую публичную семантику или соседний слой; обратимые внутренние решения принимаются по текущему коду.

## Карта данных и операций

| Обязанность | До переноса | Граница после переноса |
|---|---|---|
| Подтверждения | Поля lease, heartbeat/probe, unknown acquire, live/recovery budget в `demoSession` | `sessionEvidence`: оценка #14 и учёт времени; получение/продление связывает координатор |
| Поток | Open/probe/drain/snapshot/reconnect/cleanup вместе с ledger в `session.go` | `sessionStreamState` и lifecycle потока; принятый snapshot передаётся координатору для сверки знаний и публикации |
| Host ledger | Maps попыток/identity/in-flight/unknown, reserved Stop, saved proof и displaced; прямые изменения также из `session_service.go` и `exact_lookup.go` | Один внутренний ledger под mutex сессии; выбор/применение сверки отделены от сетевого ожидания |
| Доставка | ACK, pending/dirty/latest, coalescing, notices и long polling внутри Owner | Один внутренний механизм, который Owner создаёт, запускает и завершает; публичные методы сохраняются |
| Shutdown | Ранняя Stop, snapshot, сверка, saved proof, release, cleanup в общей сессии; отдельно выход процесса в Owner | Та же последовательность через перечисленные обязанности; отсутствие доказательства остаётся неизвестностью |

`Owner.Test` принимает историю с сохранённым `activeGeneration`; поздний ответ не заменяет новую активную Stop-пару. Generic Command и exact lookup повторно проверяют identity после сети. Исходные байты команд и canonical identity — разные сведения: Raw-вызов отправляет исходное тело, ledger сопоставляет предметную идентичность. `activeGeneration` не является stream generation; host/backend/database, host revision, core revision и sequence имеют прежние назначения.

## Синхронизация и завершение

- `demoSession.mu` защищает все части сессии. У внутренних наборов состояния нет новых mutex, горутин или самостоятельного lifecycle. `leaseGate` по-прежнему сериализует acquire/renew; shutdown использует только TryLock. При необходимости двух блокировок порядок остаётся `Owner.mu → session.mu`.
- Сеть, ожидание завершения и внешние emitters не переносятся под session mutex. `onChange`, `onEvent` и `onState` вызываются в прежнем порядке под ним; их сигналы доставки краткие и неблокирующие. Revision и gap публикуются в прежние моменты.
- Один `runStream` закрывает созданный сессией `streamStopped`; один probe worker принадлежит конкретному stream context. Отмена stream не продлевает и не освобождает lease. Snapshot/probe/heartbeat не подтверждают друг друга.
- Сессия владеет `ctx/cancel` и `actionsCtx/cancelActions`. Обычная команда повторно проверяет freshness после preflight; Stop использует отдельный допуск и контекст. Shutdown сначала отменяет обычные действия и отправляет известную Stop до потенциально зависшего snapshot; отмена всей сессии и ожидание cleanup остаются отдельными шагами.
- Доставка сохраняет прежние connection/event/watch mutex, ограниченные каналы и три существующих worker. Callback чтения сессии и внешний emitter вызываются вне delivery mutex. Connection/event завершаются по контексту Owner; status notifications — отдельным сигналом после `workerDone`. Нового join блокирующего frontend callback нет.
- Owner сохраняет startup, Verify и ссылку только на свой процесс. Его выход не заменяет подтверждения Stop/сохранения. Frontend остаётся потребителем прежнего контракта: [наблюдение](session-observation.md), [восстановление попыток](pending-recovery.md), [core contract](core-contract.md).

## U01 перед переносом Stop

На неизменённом baseline выполнен один диагностический `TestBrowserRoutesRunAndReopenCore` во временной копии: реальные browser routes, Service/Owner, отдельные core, SQLite и настоящий simulator. Обёртка simulator считала только вызовы порта Stop; диагностические метки не входят в продукт. После сохранённой завершённой Stop первый Owner/core закрыт, второй открыл ту же базу с новым backend, затем выполнен один явный POST того же Stop и точная сверка.

Получен `503 {"error":"command outcome unavailable"}`. Метка `stopSessionCommand` зафиксировала `errSessionUnavailable` до отправки; browser route сформировал HTTP 503. У второго core не было входа в Stop HTTP handler и вызова порта. Точная сверка вернула `match/completed`, record и audit неизменны, `saveStatus=saved`. Итоговые счётчики порта после подтверждённого выхода процессов: первый core — 1, второй — 0.

Identity опыта: database `d83e9690-cbab-4843-9fe6-e86ffd5211d8`, второй host `0fd7df3c-42ae-4942-bcce-d3cd5f566abd`, backend `e8c97da7-effe-401f-8e5a-453af4039e65`, test `72e3342e-13f5-4bd3-b9a2-c37c1c1935e9`, Stop `f927b733-42e6-487e-957f-c31da445e63a`; тело `{"commandId":"f927b733-42e6-487e-957f-c31da445e63a"}`. Локальные артефакты: `.tmp/.codex/host-session/u01-baseline.log`, `u01.trace`, `instrument_baseline.py` и изолированная baseline-копия.

Конкретный lease ID непосредственно в момент повтора отдельно не записан. Предшествующий `live` и успешная последующая exact lookup, требующая подтверждённого lease, дают косвенное подтверждение, но не заменяют журнал состояния lease на этой границе.

Наблюдение воспроизведено и локализовано, но не исправлено этим структурным переносом. Автоматического повтора нет; изменение смысла исторического Stop требует отдельного объёма.

## Проверяемые этапы

Выделение evidence/stream подтверждено целевыми `TestSession`, `TestDemoSession` и `TestOwnerSessionRunsAndReopensCore`, vet/lint и двумя независимыми статическими review. Устранено замечание о простой группировке stream-полей: связанные переходы начала, присоединения, отсоединения, завершения и неизвестной очистки выполняются операциями состояния под прежним mutex.

Host ledger теперь выбирает и применяет результаты сверки, подготавливает/регистрирует/завершает попытки, учитывает историю и точный результат. `session_service.go` и exact lookup используют эти операции; повторная проверка Owner/process/lease/context остаётся на прежней границе. Stream acceptance передаёт снимок ledger и не разбирает команды. Перенос прошёл полный `internal/desktop`, vet/lint.

`observationDelivery` получает только чтения status, исходного connection state и SessionView и прежние sinks. Owner запускает три прежних worker и завершает доставку в прежние моменты; ACK, coalescing, pending/latest/dirty и long polling находятся внутри механизма. Новые управляемые проверки до и после переноса подтвердили доступность fallback и Stop при блокированном connection emitter и синхронный ACK из core-event emitter. Предметные assertions существующих тестов не менялись; адрес блокировки в проверке отмены во время подготовки notice следует за перемещённым mutex.

Shutdown в `session_shutdown.go` собирает доказательства через операции ledger: начальное состояние, незавершённые попытки, применение чтений, покрытие Create, отдельная регистрация/завершение Stop и уточнение Start после сохранения. Он больше не читает карты ledger напрямую и не разбирает его JSON. Сначала отправляется известная Stop, затем читается snapshot, сверяются команды, подтверждаются Stop и saved record, после чего освобождается lease. Отмена сессии/ожидание stream cleanup и подтверждение выхода процесса по-прежнему выполняются Owner отдельно; их неуспех не подменяется успешным исходом команды.

Итоговые автоматические проверки переноса: `go test -count=1 -timeout 180s ./internal/desktop` (39,189 с), `go test -count=1 -timeout 180s ./internal/transport ./internal/browser` (17,815/14,756 с), vet/lint этих пакетов, проверка совпадения 14 generated-файлов, browser- и настоящая Windows desktop-сборки прошли. Frontend и внешний контракт не менялись; новый `check:pr` и fixtures smoke в этот host-перенос не входят. Полный предслиятельный verify не выполнялся.

Среда — Windows x64 с проектными Go 1.27.3, Wails 3.0.0, Node.js 24.22.2/npm 11.19.1. В существующем Ubuntu/WSL-контуре прошёл `go test -race -count=1 -timeout 180s ./internal/transport -run TestNativeStream` (20,566 с); тестовый процесс инструментирован, дочерний core собирается обычным `go build` без race. Сам host/desktop с race не проверен: Windows-профиль имеет CGO=0 и не содержит доступного C-компилятора; WSL-прогон штатного desktop остановился на недостающих offline-зависимостях, `pkg-config` также отсутствует. Заглушка Wails, установка GUI-зависимостей и новая инфраструктура не использовались. Журналы — `.tmp/.codex/host-session/`; реальные проходы учитываются отдельно.
