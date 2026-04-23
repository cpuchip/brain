# Brain Server — build & run
# Usage: .\start.ps1 [-SkipBuild] [-Port 8445]
#
# Prerequisites:
#   - Go 1.24+ (go build)
#   - Node.js 20+ (npm run build for frontend)
#   - .env file configured (copy .env.example)
param(
    [switch]$SkipBuild,
    [int]$Port = 8445,
    [switch]$UseGoRun
)

$ErrorActionPreference = 'Stop'
$base = $PSScriptRoot

Write-Host "`n  Brain Server" -ForegroundColor Cyan
Write-Host "  ============`n"

# Stop any running brain process
Get-Process -Name brain -ErrorAction SilentlyContinue | Stop-Process -Force

if (-not $SkipBuild) {
    # 1. Build frontend
    Write-Host "  [1/2] Building frontend..." -ForegroundColor Yellow
    Push-Location "$base\frontend"
    if (-not (Test-Path "node_modules")) {
        Write-Host "         npm install..." -ForegroundColor DarkGray
        npm install
        if ($LASTEXITCODE -ne 0) { Pop-Location; throw "npm install failed" }
    }
    npm run build
    if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Frontend build failed" }
    Pop-Location

    # 2. Build Go binary (frontend dist/ is embedded via go:embed)
    # NOTE: -tags fts5 is REQUIRED — brain uses SQLite FTS5 for full-text search.
    # Without it the binary either fails to compile (depending on go-sqlite3
    # version) or crashes at runtime on the first FTS5 query. Drift from this
    # is the most common cause of `.\start.ps1` exiting with code 1.
    Write-Host "  [2/2] Building server..." -ForegroundColor Yellow
    Push-Location $base
    go build -tags fts5 -o brain.exe ./cmd/brain/
    if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Go build failed" }
    Pop-Location
}

# Override port if specified
if ($Port -ne 8445) {
    $env:WEB_PORT = $Port
}

Write-Host "`n  Starting brain server..." -ForegroundColor Green
Write-Host "  Web UI: http://localhost:$Port" -ForegroundColor Cyan
Write-Host "  Press Ctrl+C to stop`n" -ForegroundColor DarkGray

Push-Location $base
try {
    if ($UseGoRun) {
        go run -tags fts5 ./cmd/brain/
    } else {
        & "$base\brain.exe"
    }
}
catch {
    $msg = $_.Exception.Message
    if ($msg -match "Application Control policy has blocked this file") {
        Write-Host "  brain.exe blocked by Application Control policy." -ForegroundColor Yellow
        Write-Host "  Falling back to: go run -tags fts5 ./cmd/brain/" -ForegroundColor Yellow
        go run -tags fts5 ./cmd/brain/
    } else {
        throw
    }
}
finally {
    Pop-Location
}
