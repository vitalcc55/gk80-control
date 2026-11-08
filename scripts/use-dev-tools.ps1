# Dot-source this script to select the isolated toolchain for this session only.
$nodeDir = 'tools/node-v24.22.2-win-x64'
$goDir = 'tools/go'
$goBin = 'go/bin'
$lintDir = 'tools/golangci-lint-2.15.0-windows-amd64'

$required = @(
    (Join-Path $nodeDir 'node.exe'),
    (Join-Path $nodeDir 'npm.cmd'),
    (Join-Path $goDir 'bin/go.exe'),
    (Join-Path $goBin 'wails3.exe'),
    (Join-Path $goBin 'govulncheck.exe'),
    (Join-Path $lintDir 'golangci-lint.exe')
)
$findMissingTools = {
    param([string]$root)
    $required | ForEach-Object { Join-Path $root $_ } |
        Where-Object { -not (Test-Path -LiteralPath $_ -PathType Leaf) }
}
$toolRoot = Join-Path $env:LOCALAPPDATA 'gk80-control'
$missing = @(& $findMissingTools $toolRoot)
if ($missing.Count -gt 0) {
    $checkedRoots = @($toolRoot)
    $codexPackages = @(Get-AppxPackage -Name OpenAI.Codex -ErrorAction Stop)
    if ($codexPackages.Count -gt 1) { throw 'More than one Codex package is registered for this user.' }
    if ($codexPackages.Count -eq 1) {
        # MSIX can redirect tools prepared by the agent into package-private AppData.
        $cachedRoot = Join-Path $env:LOCALAPPDATA "Packages/$($codexPackages[0].PackageFamilyName)/LocalCache/Local/gk80-control"
        $checkedRoots += $cachedRoot
        $cachedMissing = @(& $findMissingTools $cachedRoot)
        if ($cachedMissing.Count -eq 0) {
            $toolRoot = $cachedRoot
            $missing = @()
        }
    }
}
if ($missing.Count -gt 0) {
    throw "GK80 tools not found in $($checkedRoots -join ', '). Missing in the standard location: $($missing -join ', ')"
}

$nodeDir = Join-Path $toolRoot $nodeDir
$goDir = Join-Path $toolRoot $goDir
$goBin = Join-Path $toolRoot $goBin
$lintDir = Join-Path $toolRoot $lintDir
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
Write-Host "GK80 tools enabled for this PowerShell session only: $toolRoot"
