param([switch]$SharedProfile)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$hostStarted = $false
$hostExitedSafely = $false
$runDir = $null
Push-Location $repo
try {
    $runDir = Join-Path $repo ('.tmp/.codex/iab-run-' + [guid]::NewGuid().ToString('N'))
    $assetDir = Join-Path $runDir 'browser-assets'
    New-Item -ItemType Directory -Force -Path $runDir | Out-Null
    npm run build:browser -- --outDir $assetDir
    if ($LASTEXITCODE -ne 0) { throw 'Browser build failed.' }
    Copy-Item -LiteralPath (Join-Path $assetDir 'browser.html') -Destination (Join-Path $assetDir 'index.html')
    go build -o (Join-Path $runDir 'gk80-core.exe') ./cmd/gk80-core
    if ($LASTEXITCODE -ne 0) { throw 'Core build failed.' }
    go build -o (Join-Path $runDir 'gk80-browser.exe') ./cmd/gk80-browser
    if ($LASTEXITCODE -ne 0) { throw 'Browser host build failed.' }
    $browserArgs = @()
    if ($SharedProfile) { $browserArgs += '--shared-profile' }
    $hostStarted = $true
    & (Join-Path $runDir 'gk80-browser.exe') @browserArgs
    $hostExitCode = $LASTEXITCODE
    $hostExitedSafely = $hostExitCode -eq 0 -or $hostExitCode -eq 2
    exit $hostExitCode
}
finally {
    if ($runDir -and (Test-Path -LiteralPath $runDir)) {
        $base = [IO.Path]::GetFullPath((Join-Path $repo '.tmp/.codex'))
        $target = [IO.Path]::GetFullPath($runDir)
        if ([IO.Path]::GetDirectoryName($target) -ne $base -or -not [IO.Path]::GetFileName($target).StartsWith('iab-run-')) {
            throw 'Invalid browser build directory.'
        }
        if (-not $hostStarted -or $hostExitedSafely) {
            Remove-Item -LiteralPath $target -Recurse -Force
        } else {
            Write-Warning "Browser process exit was not fully confirmed. Temporary files remain at $target"
        }
    }
    Pop-Location
}
