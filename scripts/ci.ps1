#!/usr/bin/env pwsh
param(
    [switch]$RequireRace
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repoRoot

function Invoke-Step {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Action
    )
    Write-Host "==> $Name" -ForegroundColor Cyan
    $global:LASTEXITCODE = 0
    & $Action
    if ($LASTEXITCODE -ne 0) {
        throw "Step '$Name' failed with exit code $LASTEXITCODE"
    }
}

try {
    Invoke-Step "gofmt" {
        $goFiles = @(
            Get-ChildItem -Path $repoRoot -Filter "*.go" -Recurse -File |
                Where-Object {
                    $_.FullName -notmatch '([\\/])(\.git|data|node_modules|\.svelte-kit|build|dist|bin)\1'
                } |
                ForEach-Object { $_.FullName }
        )
        if ($goFiles.Count -gt 0) {
            $unformatted = @(& gofmt -l @goFiles)
            if ($LASTEXITCODE -ne 0) { return }
            if ($unformatted.Count -gt 0) {
                Write-Error ("Unformatted Go files:`n" + ($unformatted -join "`n"))
                $global:LASTEXITCODE = 1
            }
        }
    }

    Invoke-Step "go vet" {
        & go vet ./...
    }

    $hasCC = $null -ne (Get-Command gcc, clang, cc -ErrorAction SilentlyContinue | Select-Object -First 1)
    if ($hasCC) {
        Invoke-Step "go test -race" {
            $prevCgo = $env:CGO_ENABLED
            try {
                $env:CGO_ENABLED = "1"
                & go test -race ./...
            }
            finally {
                if ($null -eq $prevCgo) {
                    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
                }
                else {
                    $env:CGO_ENABLED = $prevCgo
                }
            }
        }
    }
    elseif ($RequireRace) {
        throw "No C compiler (gcc/clang/cc) found in PATH; cannot run go test -race"
    }
    else {
        Write-Warning "No C compiler found (CGO disabled); running go test ./... without -race (CI enforces -race on CGO-enabled runners)"
        Invoke-Step "go test" {
            & go test ./...
        }
    }

    Invoke-Step "checkreadme" {
        & go run ./scripts/checkreadme
    }

    if (Test-Path (Join-Path $repoRoot "web/package.json")) {
        Invoke-Step "web check" {
            & pnpm --dir web check
        }
        Invoke-Step "web build" {
            & pnpm --dir web build
        }
    }
    else {
        Write-Host "==> web check/build [skipped: web/package.json not found]" -ForegroundColor DarkGray
    }

    Write-Host "CI passed." -ForegroundColor Green
}
finally {
    Pop-Location
}
