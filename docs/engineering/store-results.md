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
