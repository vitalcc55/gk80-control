# Подтверждённые результаты Store для Engine

Основание — [#18](https://github.com/vitalcc55/gk80-control/issues/18), A06 из #11. Baseline: `11c3f4d7fc70252730c574b487616ec10ecca8d7`, актуальный main после PR #29. Исследование подтверждает связность кода; расхождение Engine и SQLite не объявляется воспроизведённым дефектом.

## Контракт и границы

Результат пяти операций явно различает статус команды, подтверждённые данные (`TestRecord`, соответствующую ревизию, ожидающее действие), новый переход и replay/no-op, отсутствие подтверждения. Engine использует фактические данные вместо арифметики над ожидаемой ревизией. Наличие результата не определяется значением ревизии или одним `err`.

Новый переход подтверждается только успешным commit. Записанный предметный отказ может возвращать статус вместе с ошибкой. Replay не выдаёт исторический snapshot за текущее состояние. При отмене до подтверждённого commit, rollback и неизвестном commit подготовленные значения не подтверждены. Последующая отмена клиентского запроса не аннулирует известный commit. Тип результата внутренний, без изменения HTTP/OpenAPI/Wails.

SQL guards, порядок записей, state+command+audit, точная идентичность, независимая Stop и восстановление сохраняются. Проверки run/controller/deadline защищают от позднего применения. Snapshot не накладывается поверх более нового оперативного состояния оборудования. Схема, Create/CreateExact, Equipment, методика, таймауты, frontend и host не меняются. Блокер — необходимость изменить эти предметные/внешние гарантии; обычные внутренние решения принимаются по коду.

## Исходные ветви

`r` — глобальная ревизия, прочитанная существующей транзакцией; `N` — число финализированных принятых команд.

| Операция | Новый commit | Replay | Успех без нового перехода | Сохранённый предметный отказ |
|---|---|---|---|---|
| Start | accepted, running, pending action, r+1 | Сохранённый статус без нового action/audit | Только replay | rejected + domain error; состояние/r не меняются, аудит — если испытание существует; иначе только команда без связанного test_id |
| Confirm | completed, pending=nil, r+1 | Сохранённый статус без действия/audit | Только replay | Аналогично Start; ошибочный action фиксируется один раз |
| CompleteStart | Start completed, running, r+1 | completed возвращает успех | completed: без commit, проверки stage и нового audit | Нет; ошибка без записи отказа |
| Complete | finished/completed/complete, FinishedAt, r+1 | После finished — ErrInvalidState | Нет | Нет; ошибка без записи отказа |
| FinishStop | Отмена pending, stopping r+1, N финализаций, итог r+2+N; completed либо failed | Прежний completed/failed без записи | Только replay | failed остановки — сохранённый исход, не ошибка транзакции |

| Ветвь ошибки | Все пять операций |
|---|---|
| Отмена до записи | Нет подтверждённого нового результата; операторские команды сохраняют ErrWriteNotStarted там, где это уже доказано |
| Ошибка обязательной записи / аудит | Rollback не подтверждает подготовленное состояние; действует прежняя блокировка дальнейших изменений |
| Неизвестный commit | Ошибка не доказывает отсутствие записи; результат уточняется существующими read-only чтениями/recovery без повторного запуска оборудования |

Оперативные `stopping`, подтверждение Equipment.Stop и естественное завершение публикуются до сохранения и отдельно меняют локальную ревизию. Они не являются доказательствами SQLite commit. Их инкременты и независимый путь Stop сохраняются.

## Проверочные поверхности

Существующие регрессии: `TestDemoRecordTransitionsAndMeasurementsSurviveReopen`, `TestConfirmedStopIsPersistedOnceAndReopens`, `TestRejectedActionIsAuditedOnce`, `TestCanceledCommandRollsBackAndStopRemainsWritable`, `TestRecoveryDoesNotResumeActiveDemo`; Engine — `TestCommittedCommandsContinueAfterClientCancellation`, `TestStopBypassesBlockedMeasurementStorage`, `TestStorageFailureStopsSimulatorWithoutClaimingSaved`, `TestRefreshPersistsCompletedRunAfterWriteDeadline`, `TestRefreshPersistsConfirmedStopAfterWriteDeadline`, `TestStartCompletionIsReportedAfterConcurrentStop`, `TestLateCommittedStartCompletionDoesNotResumeMeasurement`; core — `TestRevokedConfirmDoesNotStartSimulatorAfterCommit`, `TestExactStopLookupDoesNotStopActiveEquipment`.

Дополнения ограничены этой границей: completed no-op, rollback аудита пяти операций, результат после commit/reopen, многошаговый Stop, управляемая фактическая ревизия, неизвестный ответ после реального commit. Исходные предметные ожидания регрессий не ослабляются.

Этапы: фиксация поведения → Start/Confirm/CompleteStart → Complete/FinishStop/recovery. Каждый этап проходит целевые проверки и read-only review до commit/push. Полный verify всего проекта не запускается без предстоящего merge; обязательные пакеты/race из #18 выполняются перед передачей PR.

## U01 на текущем baseline

4 ноября 2026 года в изолированной копии baseline повторён один существующий `TestBrowserRoutesRunAndReopenCore`: реальные browser routes, Owner, отдельные core, simulator и временная SQLite. Диагностическая обёртка только считала вызовы Stop и записывала admission; изменений продукта и retry нет.

После сохранённой Stop и смены владельца один точный POST получил `503 {"error":"command outcome unavailable"}`. Host stop admission вернул `demo session is not confirmed`, до HTTP handler core. Lease `41867656-cb4f-4b93-b0b1-e9c152c120ff` был confirmed=true, до истечения срока. Exact lookup вернул match/completed; record и audit неизменны, saved. Счётчики Equipment.Stop после выхода: первый core 1, второй 0.

Database `0f3f9bd7-a67e-46d5-b02d-33f043148a80`, второй host `223842aa-3aae-4196-b2e9-7faeb76dbf09`, backend `499b76bc-90c7-417e-bd60-9a3fb4126bce`, test `8d392d31-2f39-4309-b4ab-4a188e372523`, Stop `ae8ae2f8-f990-4337-adcb-f9b36311ef52`. Проход успешен (6,630 с по пакету), наблюдение U01 воспроизведено, семантика исторической Stop не исправляется. Локальные артефакты — `.tmp/.codex/store-results/{u01-baseline.log,u01.trace,instrument_baseline.py,baseline/}`; диагностическая копия не входит в поставку.

Baseline обычных тестов Go 1.27.3 Windows: `./internal/demo` 23,561 с, `./internal/storage` 8,650 с, `./internal/core` 16,008 с. Это исходная проверка, не доказательство будущего результата. Итоговые проверки переноса записываются отдельно.

## Граница после переноса

Внутренние `storage.OperationResult` / `SavedState` не входят в генерируемый контракт. `Command` и `Saved` необязательны; `Applied` обозначает новый переход, `Replayed` — ранее известную команду/no-op. Новый записанный отказ имеет `Command`, но не `Applied`/`Saved`; его предметная ошибка сохраняется. Нулевая ревизия не обозначает отсутствие результата.

| Операция | До | После |
|---|---|---|
| Start | Engine назначал running, входное action и ExpectedRevision+1 | Применяет сохранённые record/action/revision нового перехода |
| Confirm | Engine предполагал ExpectedRevision+1 | Применяет возвращённый переход; пуск оборудования и pending-сохранение остаются оперативными фактами |
| CompleteStart | nil error приводил к revision++ и предполагаемому completed | Статус берётся из результата; новый commit возвращает своё состояние, completed no-op — явно текущее прочитанное состояние без нового перехода |
| Complete | После успеха Engine отдельно читал Test/Revision | Получает конечное состояние и ревизию из транзакции |
| FinishStop | После успеха Engine отдельно читал Test/Revision | Новый commit возвращает окончательный record и r+2+N; replay без snapshot требует отдельной сверки текущего состояния |

Применение состояния ограничено текущим run; запуск/измерение сохраняют проверки Stop, управления и срока. Поздний подтверждённый статус команды может быть опубликован без применения старого состояния. `CompleteStart` no-op возвращает текущее состояние: например finished после успешной Stop или stopping после ошибки оборудования; исторический running не восстанавливается и измерение не запускается. Оперативные Stop/естественное завершение и их локальные инкременты сохранены. Метод `PendingAction` убран только из потребительского Repository, конкретный Store и его тесты сохранены.

`refreshFinished` сначала выясняет неизвестный исход чтением. Если существующая процедура требует финализации, её новый результат применяется без последующего Test/Revision. Уже завершённая запись читается без повторной операции/Equipment. `SaveStatus` и `DataQuality` сохраняют прежний смысл. После `CompleteStart` статус saved может сразу смениться на pending первого измерительного пакета; проверка стабильной ревизии не должна требовать, чтобы этот промежуточный статус оставался неизменным.

Проверки используют реальные временные SQLite: все пять результатов после commit/reopen; отказ аудита и ошибка Commit из-за отложенного внешнего ключа во временной проверочной таблице возвращают пустой результат, после reopen изменения откатились. Это внедрение отказов только в тестах, не изменение схемы приложения. Обёртки потерянного ответа выполняют настоящую запись перед потерей результата; управляемые ревизии проверяют Engine отдельно от соответствия SQLite. Проверка старого run подменяет ссылку active под mutex, чтобы адресно проверить guard; штатный API не разрешает новое испытание при незавершённой финализации.

## Итоговая проверка переноса

4 ноября 2026 года, проектный Go 1.27.3. Этапы проходили целевые проверки, независимый read-only review, commit и push; подтверждённых замечаний к итоговому коду не осталось.

| Проверка | Фактический результат |
|---|---|
| Windows `go test -timeout 180s -count=1 ./internal/demo ./internal/storage ./internal/core` | Прошёл: 34,146 / 6,235 / 18,934 с |
| Windows `go test -timeout 180s -count=1 ./internal/transport -run TestNativeClientRunsAndReopensDemo` | Прошёл, 11,972 с; настоящий дочерний core, отдельный временный demo-профиль, reopen |
| Ubuntu/WSL `go test -race -timeout 180s -count=1 ./internal/demo ./internal/storage ./internal/core` | Прошёл: 26,760 / 6,598 / 18,672 с; CGO=1, GCC 13.3 |
| `go vet` и `golangci-lint run` для demo/storage/core/transport | Прошли, lint: 0 issues |
| Windows core и desktop Go-сборки | Прошли; desktop с `production desktopapp` и существующими статическими ресурсами |

Race инструментировал процессы `demo.test`, `storage.test`, `core.test` и используемые ими production-пакеты; core в этом наборе исполняется внутри инструментированного процесса через существующие тесты. Отдельный core процессного transport-теста собран обычным `go build` без race. Ни Windows/WebView2, ни дочерний процесс этим Linux race не инструментированы. Это новый прогон конкретных трёх пакетов #18, не прежний host race из #22.

Первый race-прогон выявил зависимость нового теста от кратковременного `SaveStatus=saved` после CompleteStart: первый пакет измерений успел законно сделать его pending. Проверка исправлена на стабильные revision/stage/число запусков, исходные регрессии не ослаблены; повторён весь требуемый race-набор. Сообщений detector о гонках не было. Дополнен режим до commit: Refresh должен применить возвращённую ревизию 101 без новых Test/Revision после успешной финализации, сохранив качество данных.

Локальные журналы и диагностические артефакты находятся в `.tmp/.codex/store-results/`: `final-tests.log`, `final-process.log`, `final-race.log`, `race-first.log`, `final-vet.log`, `final-lint.log`, `final-build-*.log`. Frontend, host, wire и миграции не менялись; frontend check:quick/check:pr, полная ручная Windows/IAB-матрица и остальные профили #10 не выполнялись. Desktop только собран, новое окно не запускалось. Полный предслиятельный verify всего проекта не выполнялся: PR передаётся на приёмку, merge не входит в эту задачу. U01 остаётся открытым наблюдением.
