$ErrorActionPreference = 'Stop'

npm run build:desktop
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Copy-Item -LiteralPath dist-desktop/desktop.html -Destination dist-desktop/index.html

New-Item -ItemType Directory -Force -Path bin | Out-Null
go build -o bin/gk80-core.exe ./cmd/gk80-core
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go build -tags 'production desktopapp' -ldflags '-H windowsgui' -o bin/gk80-desktop.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
