# Проверки качества

Команды одинаковы для терминала и действий Codex App. Подготовьте инструменты и зависимости отдельно. Обычные проверки используют локальный кэш и завершаются ненулевым кодом при ошибке. Результаты процесса, Chromium smoke и проверка окна Windows/WebView2 относятся к разным границам.

## Подготовка

Возьмите закреплённые Node/npm, Go и golangci-lint из [описания окружения](development-environment.md). На Windows подключите их командой `. .\scripts\use-dev-tools.ps1`. На Linux используйте Linux-версии инструментов и отдельную рабочую копию с собственным `node_modules`. Для подготовки Go на Linux задайте `GOENV=off`, `GOWORK=off` и `GOTOOLCHAIN=local`. Затем в корне репозитория выполните:

```text
npm ci --no-audit --no-fund
go mod download
go mod tidy -diff
```

Последний шаг подготавливает в кэше также транзитивные зависимости, которые проверяет `tidy`. В setup ему доступна сеть. Проверка `check:go` выполняет тот же расчёт с выключенным доступом к Go proxy. Разницу модульных файлов исправляют отдельной осознанной правкой.

Для `check:pr`, `check:portable` и `check:full` установите Chromium из установленного Playwright. Значение `PLAYWRIGHT_BROWSERS_PATH` при установке и проверке должно совпадать. При отсутствии переменной конфигурация тестов использует `.cache/playwright` из корня.

```powershell
$env:PLAYWRIGHT_BROWSERS_PATH = Join-Path (Get-Location) '.cache/playwright'
node node_modules/playwright/cli.js install chromium
```

```sh
export PLAYWRIGHT_BROWSERS_PATH="$PWD/.cache/playwright"
node node_modules/playwright/cli.js install --with-deps chromium
```

На Linux `--with-deps` нужен при подготовке системных библиотек Chromium. В уже подготовленном окружении достаточно `install chromium`. Для race нужны CGO и C-компилятор для выбранной платформы, для dependency scan — закреплённый govulncheck и доступ к advisory-сервисам. Wails CLI нужен для отдельных Windows-команд. В Linux переносимый профиль работает без Wails CLI и Linux GUI.

## Команды

| Профиль | Команда | Состав | Среда и результат |
|---|---|---|---|
| Быстрый frontend | `npm run check:quick` | TypeScript для исходников, тестов и конфигураций, Biome без записи и Vitest | Windows/Linux, вывод в терминал |
| Frontend перед передачей | `npm run check:pr` | `check:quick`, сборка Vite и существующий Chromium smoke | Windows/Linux с Chromium, `dist` и `test-results`. Сервер на loopback, порт 5174 должен быть свободен |
| Обычный Go | `npm run check:go` | `gofmt -l` для корневых Go-файлов, `api`, `cmd`, `internal`; `go mod tidy -diff`, `go mod verify`; закреплённый golangci-lint со штатным набором; тесты `./api/... ./cmd/... ./internal/...`; сборка `api`, `internal` и `gk80-core` | Windows/Linux, вывод в терминал. В Linux у browser entry проверяется общая часть |
| Граница host | `npm run check:host`, `npm run check:host:runner` | Production/test-графы Wails и тесты `internal/desktop`, `internal/browser`, `cmd/gk80-browser`; отдельная проверка реального отказа теста и пустого кэша | Windows/Linux, вывод в терминал. `check:host -- --race` добавляет race только для этой области |
| Переносимый набор | `npm run check:portable` | `check:pr`, `check:go`, `check:host`, `check:host:runner`. Host-тесты повторяются с явной проверкой графов | Windows/Linux с frontend и Go, результаты составляющих команд |
| Нативный набор | `npm run check:windows` | `check:host:native` с настоящими Wails bindings, desktop-сборка, tagged lint и отдельный корневой тест классификатора с `desktopapp`; сборка browser host с ресурсами, `contract:check` и `contract:test` | Windows; EXE в `bin`, отдельный browser bundle в `.tmp/.codex/browser-build-*`, вывод генератора. На Linux запрос завершается ошибкой |
| Локальный предслиятельный набор Windows | `npm run check:full` | `check:windows` и `check:portable` | Windows, результаты составляющих команд. На Linux используйте `check:portable` |

`check:host:native` проверяет `internal/desktop`, включая графы Wails и binding-тесты. Корневой тест входит в `check:windows` после подготовки ресурсов `dist-desktop`. `contract:check` сравнивает сохранённые DTO и bindings с генерацией во временной копии. `contract:test` подтверждает применение изменённой схемы к Go и TypeScript. Обе команды требуют Windows. `contract:generate` явно обновляет сгенерированные файлы. Детали границы host приведены в [host-checks.md](host-checks.md).

Go-тесты обычных профилей идут с `-count=1 -shuffle=on`. При падении сохраните строку `-test.shuffle` из вывода пакета. Повтор отдельного пакета можно выполнить с `go test -count=1 -shuffle=<seed> ./internal/core`. Сценарий `TestOwnerSessionRunsAndReopensCore` входит в переносимый набор. Он запускает отдельный demo-core, использует симулятор и временный профиль SQLite, затем повторно открывает данные.

## Расширенные проверки

- **Race** — `npm run check:race`. Запускает тесты `internal/browser`, `internal/core`, `internal/demo`, `internal/desktop`, `internal/diagnostics`, `internal/storage` и `internal/transport` с `-race` и CGO. Дочерний core, который процессные тесты собирают обычным `go build`, выполняется без инструментации. При отсутствии компилятора команда завершится ошибкой. Linux race проверяет переносимую Go-часть.
- **Coverage** — `npm run check:coverage`. Запускает весь переносимый Go-набор с `-coverpkg` для `internal/contract`, `internal/core` и `internal/demo`. Процент операторов Go — отчётная метрика без блокирующего порога. Из расчёта исключён только generated `internal/contract/models.gen.go`. Тестовые файлы Go не входят в профиль операторов. Запуск дочернего core без инструментации не добавляет исполненные им операторы в отчёт.
- **Frontend coverage** выполняется в той же команде через V8 из закреплённого Vitest. Порог 90% относится к сумме ветвлений в явно перечисленных файлах `src/browser/BrowserApp.tsx`, `src/components/{ParametersScreen,TestsScreen}.tsx`, `src/desktop/{DesktopApp,History}.tsx` и `src/desktop/{browser-bridge,browser-responses,command-attempt-state,demo-input,demo-service}.ts`. Включены целые файлы, в том числе ветви форм для подготовленных состояний. Отдельные файлы fixtures, тестов и generated вне этой области. Отчёт учитывает выбранные исходники даже без импорта в тестах. Исходный Go-профиль, JSON и HTML frontend находятся в `.tmp/.codex/coverage-*`. Ошибки тестов и инструментов, некорректный Go-отчёт и недостижение порога frontend дают ненулевой код.
- **Fuzz** — `npm run check:fuzz`. `FuzzCanonicalJSONNumber` проверяет точное значение и идентичность эквивалентных числовых записей, `FuzzInspectStartCommand` проверяет подтверждения запуска и воспроизводимость канонической команды. Для каждой цели выделено 10 секунд, до двух workers и до трёх секунд минимизации. Go пишет найденный пример в `testdata/fuzz/<цель>` внутри выведенного временного каталога `.tmp/.codex/fuzz-*`. Для воспроизведения из этого каталога выполните `go test -run '<цель>/<имя-примера>' ./internal/contract`. Успешный запуск удаляет свою копию.
- **Зависимости** — `npm run check:deps`. Сетевой проход сохраняет `summary.json`, исходные stdout/stderr, дату UTC, SHA, признак изменённого дерева и версии в `.tmp/.codex/dependencies-*`. Govulncheck анализирует Go production-граф переносимых пакетов, а на Windows ещё граф `production,desktopapp` с уже собранным `dist-desktop`. В Go различаются вызов символа, импорт пакета и совпадение модуля. Тесты и генераторы вне Go-области. npm audit проверяет lock-граф вместе с dev, optional и peer; в отчёте сохраняются роли узлов и источник advisory. Достижимость в приложении npm audit не анализирует. Находки и невозможность проверить применимый граф дают ненулевой код и разные статусы. Linux явно отмечает Windows-область как неприменимую.

Решением пользователя от 08.11.2026 для #39 исходная цель 90% операторов Go заменена отчётной метрикой: принят текущий уровень около 80% (Windows 79,95%, Linux 80,01% на предыдущих прогонах). Это не строгий минимум 80% и не покрытие всех ветвлений. Проверка исходников и существующих assertions команд, точной сверки, остановки, истории и валидации не выявила конкретного пробела, блокирующего приёмку инфраструктуры. Часть этих сценариев проверяется через отдельный core и не входит в процент. Область расчёта и исключения сохранены; новые тесты добавляют по подтверждённому риску поведения.

Расширенные профили запускаются отдельно по изменению и перед приёмкой соответствующей области. Сетевой проход зависит от состояния advisory-баз на момент запуска. Ручной Windows/WebView2 и IAB-проходы относятся к приёмке приложения.

## Codex App и отчёт

Локальное окружение `ГК80` хранится в [environment.toml](../../.codex/environments/environment.toml). Setup worktree устанавливает зависимости из lock-файла и подготавливает Go-кэш, затем завершается. Chromium подготавливается отдельно. В PowerShell действия «Проверить frontend» и «Проверить Go» вызывают те же `check:quick` и `check:go`. «Запустить демо интерфейса» отдельно запускает `ui:fixtures` на `127.0.0.1:5173`. Настройка доступна в Local Environments. [Документация OpenAI](https://learn.chatgpt.com/docs/environments/local-environment)

Для каждого прогона укажите `git rev-parse HEAD`, чистоту дерева, ОС, точную команду и результат. Например:

```text
<SHA>, чистое дерево, Ubuntu 24.04 / WSL2
npm run check:portable — код 0, Vitest/Chromium и процессный сценарий с SQLite пройдены
npm run check:full — код 1, нативная проверка требует Windows
Артефакты: лог прогона и test-results
```

`npm run iab:demo` запускает настоящий demo-core с симулятором и SQLite для отдельного локального IAB-прохода. Укажите отдельный абсолютный каталог в `GK80_DEMO_PROFILE_DIR`, если нужен изолированный demo-профиль. Полный журнал ранней приёмки хранится в [first-core-demo-verification.md](first-core-demo-verification.md).
