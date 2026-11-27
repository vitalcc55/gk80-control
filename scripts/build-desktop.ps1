$ErrorActionPreference = 'Stop'

npm run build:desktop
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Copy-Item -LiteralPath dist-desktop/desktop.html -Destination dist-desktop/index.html

New-Item -ItemType Directory -Force -Path bin | Out-Null
go build -trimpath -tags 'production desktopapp' -ldflags '-H windowsgui' -o bin/gk80-desktop.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
