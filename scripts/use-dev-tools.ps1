# Dot-source this script to select the isolated toolchain for this session only.
$toolRoot = Join-Path $env:LOCALAPPDATA 'gk80-control'
$nodeDir = Join-Path $toolRoot 'tools/node-v24.22.2-win-x64'
$goDir = Join-Path $toolRoot 'tools/go'
$goBin = Join-Path $toolRoot 'go/bin'
$lintDir = Join-Path $toolRoot 'tools/golangci-lint-2.15.0-windows-amd64'

$required = @(
    (Join-Path $nodeDir 'node.exe'),
    (Join-Path $nodeDir 'npm.cmd'),
    (Join-Path $goDir 'bin/go.exe'),
    (Join-Path $goBin 'wails3.exe'),
    (Join-Path $goBin 'govulncheck.exe'),
    (Join-Path $lintDir 'golangci-lint.exe')
)
$missing = @($required | Where-Object { -not (Test-Path -LiteralPath $_ -PathType Leaf) })
if ($missing.Count -gt 0) {
    throw "GK80 tools not found: $($missing -join ', ')"
}

$env:GOROOT = $goDir
$env:GOPATH = Join-Path $toolRoot 'go'
$env:GOBIN = $goBin
$env:GOCACHE = Join-Path $toolRoot 'cache/go-build'
$env:GOMODCACHE = Join-Path $toolRoot 'cache/go-mod'
$env:GOENV = 'off'
$env:GOTOOLCHAIN = 'local'
$env:npm_config_prefix = Join-Path $toolRoot 'npm-global'
$env:npm_config_cache = Join-Path $toolRoot 'cache/npm'
$env:PATH = "$nodeDir;$goDir\bin;$goBin;$lintDir;$env:PATH"
Write-Host 'GK80 tools enabled for this PowerShell session only.'
