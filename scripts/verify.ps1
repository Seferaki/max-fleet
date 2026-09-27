param(
    [ValidateSet('contract', 'gateway', 'web', 'docker', 'all')]
    [string]$Direction = 'all'
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path

function Assert-Exit([string]$Step) {
    if ($LASTEXITCODE -ne 0) { throw "$Step завершился с кодом $LASTEXITCODE" }
}

function Verify-Contract {
    Push-Location $repoRoot
    try {
        py contracts/validate.py
        Assert-Exit 'Контракт и fixtures'
        npx --yes '@redocly/cli@2.54.3' lint contracts/data-api.openapi.yaml contracts/map-api.openapi.yaml --config redocly.yaml --format=stylish
        Assert-Exit 'Redocly lint'
    } finally { Pop-Location }
}

function Verify-Gateway {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    $goPath = if ($goCommand) { $goCommand.Source } else {
        Join-Path $repoRoot '.local/go-dist/go/bin/go.exe'
    }
    if (-not (Test-Path -LiteralPath $goPath -PathType Leaf)) {
        throw 'Go CLI отсутствует. Нужен Go 1.27.1 или официальный архив в .local/go-dist.'
    }
    Push-Location (Join-Path $repoRoot 'services/gateway')
    try {
        & $goPath test ./...
        Assert-Exit 'go test'
        & $goPath vet ./...
        Assert-Exit 'go vet'
        & $goPath build ./cmd/gateway ./cmd/data-mock
        Assert-Exit 'go build'
    } finally { Pop-Location }
}

function Verify-Web {
    Push-Location (Join-Path $repoRoot 'web')
    try {
        npm ci --no-audit --no-fund
        Assert-Exit 'npm ci'
        npm run typecheck
        Assert-Exit 'typecheck'
        npm run build
        Assert-Exit 'web build'
    } finally { Pop-Location }
}

function Verify-Docker {
    docker info --format '{{.ServerVersion}}' 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Docker Engine недоступен. Контейнерные сборки не проверены.'
    }
    Push-Location $repoRoot
    try {
        docker build -f services/gateway/Dockerfile.gateway -t max-fleet-gateway:scaffold services/gateway
        Assert-Exit 'Docker gateway'
        docker build -f services/gateway/Dockerfile.data-mock -t max-fleet-data-mock:scaffold services/gateway
        Assert-Exit 'Docker data-mock'
        docker build -f web/Dockerfile -t max-fleet-web:scaffold web
        Assert-Exit 'Docker web'
    } finally { Pop-Location }
}

if ($Direction -in @('contract', 'all')) { Verify-Contract }
if ($Direction -in @('gateway', 'all')) { Verify-Gateway }
if ($Direction -in @('web', 'all')) { Verify-Web }
if ($Direction -in @('docker', 'all')) { Verify-Docker }
Write-Output "verify $Direction`: OK"
