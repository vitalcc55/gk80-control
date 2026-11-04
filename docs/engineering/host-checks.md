# Переносимые проверки host

## Контракт и исходная граница

Основание — #22 и отложенный штатный host race после #17. Baseline — `df983f89cc5bc73ff6d61f52e87f711d407c491b`. Результат: один и тот же production host и содержательные регрессии выполняются на Windows/Linux без GUI Wails; нативная регистрация и её проверки остаются отдельной Windows-границей.

Проверяемые поверхности: графы production/test зависимостей, реальные тесты `internal/desktop`, `internal/browser`, общей части `cmd/gk80-browser`, Linux race этих пакетов, Windows bindings, штатная генерация и адресный WebView2-проход. Публичные типы, методы, события и RPC ID, синхронизация, lease/freshness, ledger, Stop и таймауты сохраняются. Новый Linux executable, GUI, алгоритмы, SQLite, U01 и полный объём #10 не входят в изменение. Препятствие, требующее отдельного решения: необходимость менять публичную или прикладную семантику ради переноса.

До переноса обычный Windows `go list -deps -test` трёх пакетов включал `github.com/wailsapp/wails/v3/pkg/application`. После отдельной подготовки модулей baseline Linux `go test -run '^$' ./internal/desktop` дошёл до сборки Wails и завершился ошибкой отсутствующего `pkg-config`. Это GUI-зависимость; она отличается от прежнего неполного offline-кеша и Windows `CGO_ENABLED=0` без C-компилятора.

| Область | До | После |
|---|---|---|
| `Service`, `BootstrapService` | Тела методов вместе с импортом Wails | Те же типы и методы в прежнем `internal/desktop`, без GUI |
| Регистрация | Конструкторы Wails и `RegisterEvent` в обычных файлах | `wails_registration_windows.go`, явный `desktopapp` |
| Binding-тесты | Два настоящих Wails-теста в общем `command_input_test.go` | Неизменённые проверки в `wails_binding_windows_test.go`, явный `desktopapp` |
| Декодеры/команды | Общие и Wails-тесты смешаны | Все чистые проверки остались в общем наборе |
| Browser entry | Общие `report`/`waitReady` и два теста только в Windows-файлах | Те же функции и тесты в `readiness.go`/`readiness_test.go`; `main`, `run`, Winsock и `.exe` остаются Windows-only |
| Генерация | Неявно сканирует обычную регистрацию | Windows-генератор явно включает `desktopapp`, требует прежние 2 сервиса, 24 метода, 3 события и сравнивает все generated-файлы |

Linux-профиль проверяет общую часть команды browser, а не сборку/запуск Linux browser executable. Процессные проверки `Owner`, сессии и browser routes не исключаются: они запускают настоящий отдельный core на временных demo-профилях. Windows-only проверки завершения процесса остаются Windows-only.

## Запуск

Обе ОС используют закреплённые Go 1.27.3, Node 24.22.2 и npm 11.19.1. После отдельного setup из [development-environment.md](development-environment.md) и `go mod download`:

```text
npm run check:host
npm run check:host -- --race
npm run check:host:runner
```

Обычный и race-профиль выбирают одни и те же три пакета. Сначала отдельно проверяются `go list -deps` и `go list -deps -test` с теми же флагами native/race, затем запускается весь набор `go test -count=1 -timeout=240s` без фильтров. Timeout относится к каждому тестовому процессу Go, а не к длительности всего npm-профиля. Профиль задаёт `GOPROXY=off`, `GONOPROXY=none`, `GOWORK=off`, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`: не устанавливает инструменты/модули, не изменяет модульные файлы, не наследует внешний workspace или скрытый `-run`/`-skip`/build tag. Ошибка запуска, сигнал или ненулевой код Go дают неуспех профиля. Новые флаги, кроме `--race`/`--native`, отклоняются.

Отрицательная проверка создаёт временную копию только открытых исходников, добавляет настоящий падающий Go-тест и задаёт наследуемые `GOFLAGS=-run=^$` и `GOWORK` с другим каталогом модуля. Тот же профиль обязан вернуть ненулевой код с уникальным текстом именно этого теста. Вторая проверка использует пустой модульный кеш и разрешающие прямую загрузку внешние настройки: ожидается адресный отказ offline без скачанных архивов. Go и Wails не подменяются; оригиналы и private-sources не копируются и не изменяются.

Только на Windows:

```text
npm run check:host:native
npm run contract:check
npm run contract:test
```

Native-профиль требует Wails `application` в обоих графах и запускает весь `internal/desktop` с `-tags=desktopapp`, включая два настоящих binding-теста. Linux получает явную ошибку. Генерация выполняется штатным Windows Wails CLI 3.0.0, проверяет оба сервиса, 24 метода и все три события, включая `BootstrapService`, и побайтовое соответствие Go DTO/TypeScript/RPC ID. Значения относятся к принятому контракту, а не к произвольному будущему набору методов.

`owner_failure_windows_test.go` остаётся дополнительной платформенной частью host-пакета на Windows: проверяет аварийный выход своего процесса и диагностику. Поздний Verify проверяет общий `TestDelayedVerificationCannotRestoreExitedCore` в `owner_test.go`. Общие session/ledger/exact lookup/recovery/delivery/process-тесты не помечены новым тегом и не исключены. Linux GUI, сборка Linux browser executable и весь `check:pr` не входят в этот профиль. Frontend-профиль остаётся самостоятельным.

## Результаты этапа отделения

Первый этап проверен в Windows 11 x64 и Ubuntu 24.04.3/WSL2, Go 1.27.3. `go test -count=1 -timeout 240s ./internal/desktop ./internal/browser ./cmd/gk80-browser` прошёл на обеих ОС (desktop: 38,111/43,798 с; browser: 15,816/34,580 с). Production- и test-графы обоих окружений не содержат Wails `application`.

На Windows прошли оба перенесённых настоящих binding-теста с `-tags desktopapp`, `contract:check` (14 файлов совпадают), vet/lint трёх пакетов. Два read-only review не нашли подтверждённых проблем. Generated и lock-файлы не менялись.

## Результаты профиля и нативной границы

Общий профиль выполнен в Windows 11 x64 и Ubuntu 24.04.3/WSL2 (ядро 6.6.87.2), с указанными закреплёнными версиями. Для Linux подготовлен официальный Node-архив, проверенный по SHA-256, использован существующий Go 1.27.3 и GCC 13.3; модули скачаны отдельным setup. Окончательный Windows-профиль после review: desktop 43,795 с, browser 15,049 с, cmd 4,694 с. Linux: desktop 44,096 с, browser 30,136 с, cmd 0,011 с. Оба графа каждой ОС не содержат Wails.

Оба отрицательных runner-теста прошли на Windows и Linux: настоящий `t.Fatal` вернулся через профиль несмотря на наследуемые фильтр/workspace, пустой кеш дал offline-отказ без скачанных архивов при разрешающих прямую загрузку настройках. Отдельно отсутствие Go дало ненулевой код с ENOENT. Native-профиль прошёл весь пакет desktop (38,353 с), требуя GUI в обоих графах. `contract:test` прошёл изменение схемы в Go/TypeScript за одну генерацию и отклонение `any`; `contract:check` подтвердил отсутствие generated drift. Windows vet и lint, включая `desktopapp`, прошли. Сборки browser frontend/browser-host и настоящего Windows core/desktop прошли.

Окончательный Linux `npm run check:host -- --race` выполнил `go test -count=1 -timeout=240s -race ./internal/desktop ./internal/browser ./cmd/gk80-browser`: прошёл за 42,045/32,239/1,040 с по пакетам; оба графа с активным `-race` также без GUI. Инструментированы реальные тестовые процессы host/browser и используемые ими production-пакеты. Дочерние core в существующих процессных тестах собираются обычным `go build` без `-race`: этот результат не доказывает race-безопасность core или WebView2. Тем самым отложенный штатный host race после #17 выполнен новым прогоном #22; историческое отсутствие этой проверки в PR #28 не меняется.

Настоящее окно Windows/WebView2 собрано на `e742f2a9e7c99efb37268833f6169c5e60772b72`; последующие изменения профиля и документации не меняют Go-код приложения. На отдельном demo-профиле выполнены Create, Start, Confirm, меняющиеся измерения и Stop с результатом «Остановка подтверждена ядром»/«Сохранено» для test `64721f37-7f9f-4256-b633-958511cd1be7`. Два наблюдения показаний имеют разные значения и времена с интервалом 13 с. Обновление UI подтверждено; конкретный источник каждого кадра между Wails-подпиской и bounded fallback не различался. После штатного закрытия desktop PID 45052 и принадлежащий ему core PID 8100 отсутствуют; JSONL содержит `core.stopped` с `exit_code=0` и завершение обоих процессов. Computer Use освобождён.

Локальные артефакты находятся в `.tmp/.codex/portable-host/`: baseline/графы, setup, boundary/profile/race/negative/native/contract/build logs, `native-*.txt`, identity/PID и JSONL отдельного demo-профиля. Это реальные Go/процессные и Windows-проверки; fixtures и новый полный IAB-проход не выполнялись. Generated/frontend-адаптеры не менялись, поэтому `check:pr` не повторялся. Полный предслиятельный verify остаётся отдельным будущим шагом. U01, нативный remount, аннотации и полная приёмка #6/#7 этим этапом не объявляются выполненными.
