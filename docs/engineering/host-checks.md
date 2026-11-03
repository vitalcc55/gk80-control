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

## Результаты

Первый этап проверен в Windows 11 x64 и Ubuntu 24.04.3/WSL2, Go 1.27.3. `go test -count=1 -timeout 240s ./internal/desktop ./internal/browser ./cmd/gk80-browser` прошёл на обеих ОС (desktop: 38,111/43,798 с; browser: 15,816/34,580 с). Production- и test-графы обоих окружений не содержат Wails `application`.

На Windows прошли оба перенесённых настоящих binding-теста с `-tags desktopapp`, `contract:check` (14 файлов совпадают), vet/lint трёх пакетов. Два read-only review не нашли подтверждённых проблем. Generated и lock-файлы не менялись.

Общий npm-профиль, отрицательный сценарий runner, race и адресная оконная приёмка фиксируются следующим проверяемым этапом. Историческое ограничение PR #28 не считается закрытым одним переносом регистрации: для него нужен отдельный фактический race реального host.
