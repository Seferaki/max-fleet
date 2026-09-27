$ErrorActionPreference = 'Stop'

$secretDirectory = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'MAXFleet\secrets'
$currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$names = @(
    'max_webhook_secret',
    'data_api_token',
    'worker_api_token',
    'postgres_password',
    'data_runtime_password',
    'migration_password',
    's3_access_key',
    's3_secret_key'
)

function Set-PrivateAcl([string]$Path, [bool]$Directory) {
    $sid = $currentSid.Value
    $grant = if ($Directory) { "*${sid}:(OI)(CI)F" } else { "*${sid}:F" }
    & icacls.exe $Path /inheritance:r /grant:r $grant | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Не удалось ограничить ACL: $Path" }
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected -or
        @($acl.Access | Where-Object {
            $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value -ne $sid
        }).Count -ne 0) {
        throw "ACL содержит посторонние правила: $Path"
    }
}

New-Item -ItemType Directory -Path $secretDirectory -Force | Out-Null
Set-PrivateAcl -Path $secretDirectory -Directory $true

foreach ($name in $names) {
    $path = Join-Path $secretDirectory $name
    if (Test-Path -LiteralPath $path) {
        if ((Get-Item -LiteralPath $path).Length -eq 0) {
            throw "Пустой существующий файл: $name"
        }
        Set-PrivateAcl -Path $path -Directory $false
        Write-Output "$name`: уже существует"
        continue
    }

    $bytes = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    $value = [BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant()
    $stream = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try {
        $encoded = [Text.UTF8Encoding]::new($false).GetBytes($value)
        $stream.Write($encoded, 0, $encoded.Length)
    }
    finally {
        $stream.Dispose()
        $value = $null
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
    Set-PrivateAcl -Path $path -Directory $false
    Write-Output "$name`: создан"
}

Write-Output 'MAX token вводится отдельно: scripts/enter-max-token.ps1 -Enter'
