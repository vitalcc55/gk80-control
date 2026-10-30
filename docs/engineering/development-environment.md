# Локальное окружение разработки

Для ГК80 на Windows используется отдельный набор инструментов в `%LOCALAPPDATA%\gk80-control`. Он не заменяет системные установки и не требует изменения постоянного `PATH`. Перед работой откройте новую сессию PowerShell в корне проекта и выполните:

```powershell
. .\scripts\use-dev-tools.ps1
node --version
npm --version
go version
wails3 version
golangci-lint version
govulncheck -version
```

Скрипт изменяет переменные только текущего процесса. В том числе отдельный `npm_config_prefix` исключает подхват другого глобального npm, а каталоги Go и кэши принадлежат этому окружению. Закрытие окна завершает действие настроек. Для новой копии проекта инструменты также должны быть подготовлены на соответствующем ПК: скрипт их не скачивает.

## Подготовка другого Windows-ПК

Откройте ветку `codex/first-core-demo`: текущие реализация, [план](first-core-demo-plan.md) и [журнал проверок](first-core-demo-verification.md) находятся в ней, а не в `main`. На новом ПК установите Git и WebView2 Runtime, затем подготовьте версии из таблицы ниже. Сверяйте SHA-256 архивов с опубликованными у их поставщиков. Для Windows x64 распакуйте архивы Node.js, Go и golangci-lint в общий каталог `%LOCALAPPDATA%\gk80-control\tools`: каждый архив уже содержит свой корневой каталог. Проверьте получившиеся пути исполняемых файлов по `scripts/use-dev-tools.ps1` и версию npm.

До первого запуска этого скрипта установите Wails и govulncheck в его каталог Go, используя подготовленный Go в текущей сессии PowerShell:

```powershell
$toolRoot = Join-Path $env:LOCALAPPDATA 'gk80-control'
$env:GOROOT = Join-Path $toolRoot 'tools/go'
$env:GOPATH = Join-Path $toolRoot 'go'
$env:GOBIN = Join-Path $env:GOPATH 'bin'
$env:GOENV = 'off'
$env:GOTOOLCHAIN = 'local'
& (Join-Path $env:GOROOT 'bin/go.exe') install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
& (Join-Path $env:GOROOT 'bin/go.exe') install golang.org/x/vuln/cmd/govulncheck@v1.9.0
```

Затем подключите `use-dev-tools.ps1`, проверьте версии и `wails3 doctor`; установку зависимостей, Chromium и команды проверок см. в [quality-workflow.md](quality-workflow.md). Проверки на прежнем ПК не доказывают готовность новой машины. Оригиналы ТЗ и паспортов, локальный demo-профиль, временные трассы и диагностические сборки в Git не входят; оригиналы при необходимости передаются отдельно, а для новых проверок можно использовать новый временный профиль.

## Закреплённые инструменты

| Инструмент | Версия | Источник |
|---|---|---|
| Node.js и npm | 24.22.2 и 11.19.1 | [Node.js](https://nodejs.org/dist/v24.22.2/) |
| Go | 1.27.3 | [Go](https://go.dev/dl/) |
| Wails CLI | 3.0.0 | [Wails](https://github.com/wailsapp/wails/releases/tag/v3.0.0) |
| golangci-lint | 2.15.0 | [golangci-lint](https://github.com/golangci/golangci-lint/releases/tag/v2.15.0) |
| govulncheck | 1.9.0 | [Go Vulnerability Database tools](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) |

На этом ПК Windows-архивы Node.js, Go и golangci-lint проверяются по опубликованным SHA-256. Wails и govulncheck устанавливаются через `go install` с указанными версиями и отдельными каталогами Go. Версии CLI Wails, Go-модуля и frontend runtime при создании приложения должны совпадать.

Зависимости frontend устанавливаются в репозиторий вместе с его lock-файлом. Chromium для Playwright и C-компилятор для Go race-тестов подготавливаются при появлении соответствующих проверок. `wails3 doctor` показывает состояние платформенных зависимостей; его результат не заменяет сборку и проверку приложения на Windows.

## Проверка на рабочем ПК

29 октября 2026 года в новой сессии проверены пути и версии команд, npm и Go выполнили тесты во временном проекте, golangci-lint и govulncheck проверили этот проект. `wails3 doctor` сообщил о готовности к разработке и обнаружил WebView2 Runtime 156.0.4314.50; инструменты упаковки и подписи отсутствуют. Отдельный временный стандартный проект Wails 3.0.0 собран для Windows с `CGO_ENABLED=0`, без C-компилятора; это не сборка приложения ГК80. IAB открывает локальный прототип, аннотации использованы для замечаний и повторного просмотра.

Проверены Windows 11 Pro 25H2 x64, Git 2.55.0.windows.5, Codex App 26.1023.6621.0 и Codex CLI 0.147.0. Проектный пользовательский каталог `%LOCALAPPDATA%\GK80\demo-gk80-control\logs` создан и проверен на запись, чтение и удаление тестового файла без повышенных прав. Он находится вне Git. Для параллельных рабочих копий применяйте разные профили, не общую demo-базу.

В отдельной временной worktree проверено: игнорируемые оригиналы в неё автоматически не попадают, но в данной среде доступны из исходной рабочей папки. Указатель имён, версий и назначений находится в игнорируемом `docs/private-sources/INDEX.local.md`; оригиналы нужно безопасно предоставить исполнителю отдельно. Проверка не подтверждает их наличие на другом ПК.

Обычная сборка Wails и используемая SQLite без CGO не требуют GCC. GCC для Windows Go race-тестов ещё не установлен и будет нужен при подготовке этих тестов. Действия local environment настраиваются средствами Codex App; настройка и выполнение действий проекта пока не подтверждены. Актуальные результаты desktop и core и оставшиеся проверки находятся в [журнале](first-core-demo-verification.md) и [плане](first-core-demo-plan.md).
