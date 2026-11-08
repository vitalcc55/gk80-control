$ErrorActionPreference = 'Stop'
$bootstrap = Join-Path $PSScriptRoot 'use-dev-tools.ps1'
$scratch = Join-Path (Split-Path -Parent $PSScriptRoot) ('.tmp/.codex/dev-tools probe ' + [guid]::NewGuid().ToString('N'))
$requiredFiles = @(
    'tools/node-v24.22.2-win-x64/node.exe',
    'tools/node-v24.22.2-win-x64/npm.cmd',
    'tools/go/bin/go.exe',
    'go/bin/wails3.exe',
    'go/bin/govulncheck.exe',
    'tools/golangci-lint-2.15.0-windows-amd64/golangci-lint.exe'
)
$envNames = @('LOCALAPPDATA', 'GOROOT', 'GOPATH', 'GOBIN', 'GOCACHE', 'GOMODCACHE', 'GOENV', 'GOTOOLCHAIN', 'npm_config_prefix', 'npm_config_cache', 'PATH')
$original = @{}
foreach ($name in $envNames) { $original[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

function Get-AppxPackage {
    param([string]$Name)
    if ($Name -ne 'OpenAI.Codex') { throw 'Unexpected package lookup' }
    if ($script:rejectPackageLookup) { throw 'Complete standard tools must not need Appx' }
    if ($script:packageRegistered) { [pscustomobject]@{ PackageFamilyName = 'OpenAI.Codex_fixture' } }
}

try {
    foreach ($scenario in @('standard', 'packaged', 'incomplete', 'unregistered')) {
        $caseRoot = Join-Path $scratch $scenario
        $standard = Join-Path $caseRoot 'gk80-control'
        $packaged = Join-Path $caseRoot 'Packages/OpenAI.Codex_fixture/LocalCache/Local/gk80-control'
        $script:rejectPackageLookup = $scenario -eq 'standard'
        $script:packageRegistered = $scenario -ne 'unregistered'
        foreach ($root in @($standard, $packaged)) {
            foreach ($file in $requiredFiles) {
                # Incomplete roots deliberately miss different files; their tools must not be combined.
                if ($scenario -ne 'standard' -and $root -eq $standard -and $file -eq $requiredFiles[0]) { continue }
                if ($scenario -eq 'incomplete' -and $root -eq $packaged -and $file -eq $requiredFiles[2]) { continue }
                $path = Join-Path $root $file
                [void][IO.Directory]::CreateDirectory((Split-Path -Parent $path))
                [IO.File]::WriteAllBytes($path, [byte[]]@())
            }
        }
        foreach ($name in $envNames) { [Environment]::SetEnvironmentVariable($name, $original[$name], 'Process') }
        $env:LOCALAPPDATA = $caseRoot
        $beforePath = $env:PATH
        $failed = $false
        try { . $bootstrap } catch {
            if ($_.Exception.Message -notlike 'GK80 tools not found in *') { throw }
            $failed = $true
        }
        $expectedFailure = $scenario -in @('incomplete', 'unregistered')
        if ($failed -ne $expectedFailure) { throw "Wrong bootstrap result for $scenario" }
        if ($failed) {
            if ($env:PATH -ne $beforePath) { throw "Failed bootstrap changed PATH for $scenario" }
        } else {
            $expected = if ($scenario -eq 'standard') { $standard } else { $packaged }
            if ($env:GOROOT -ne (Join-Path $expected 'tools/go') -or
                $env:GOPATH -ne (Join-Path $expected 'go') -or
                $env:GOCACHE -ne (Join-Path $expected 'cache/go-build') -or
                -not $env:PATH.StartsWith((Join-Path $expected 'tools/node-v24.22.2-win-x64') + ';')) {
                throw "Bootstrap mixed tool roots for $scenario"
            }
        }
        Write-Host "PASS bootstrap $scenario"
    }
} finally {
    foreach ($name in $envNames) { [Environment]::SetEnvironmentVariable($name, $original[$name], 'Process') }
    if (Test-Path -LiteralPath $scratch) {
        $target = [IO.Path]::GetFullPath($scratch)
        $parent = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $PSScriptRoot) '.tmp/.codex'))
        if ([IO.Path]::GetDirectoryName($target) -ne $parent -or -not [IO.Path]::GetFileName($target).StartsWith('dev-tools probe ')) {
            throw 'Invalid bootstrap probe directory'
        }
        Remove-Item -LiteralPath $target -Recurse -Force
    }
}
