param([ValidateSet('data_api_token', 'worker_api_token')][string]$Name = 'data_api_token')
$ErrorActionPreference = 'Stop'

$secretDirectory = if ([string]::IsNullOrWhiteSpace($env:MAX_FLEET_SECRETS_DIR)) {
    Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'MAXFleet\secrets'
} else {
    if (-not [System.IO.Path]::IsPathRooted($env:MAX_FLEET_SECRETS_DIR)) {
        throw 'MAX_FLEET_SECRETS_DIR must be an absolute path outside the repository.'
    }
    [System.IO.Path]::GetFullPath($env:MAX_FLEET_SECRETS_DIR)
}
$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$repositoryPrefix = $repositoryRoot + [System.IO.Path]::DirectorySeparatorChar
if ($secretDirectory.Equals($repositoryRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
    $secretDirectory.StartsWith($repositoryPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'MAX_FLEET_SECRETS_DIR must be outside the repository.'
}
$source = Join-Path $secretDirectory $Name
if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    & (Join-Path $PSScriptRoot 'bootstrap.ps1') | Out-Null
}
if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "$Name is missing after bootstrap"
}

$directory = Join-Path (Split-Path -Parent $secretDirectory) 'compose-secrets'
$target = Join-Path $directory $Name
New-Item -ItemType Directory -Path $directory -Force | Out-Null
Copy-Item -LiteralPath $source -Destination $target -Force

$userSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
& icacls.exe $directory /inheritance:r /grant:r "*${userSid}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Cannot restrict compose secret directory ACL' }
& icacls.exe $target /inheritance:r /grant:r "*${userSid}:F" '*S-1-5-18:F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Cannot restrict compose secret file ACL' }
$acl = Get-Acl -LiteralPath $target
if (-not $acl.AreAccessRulesProtected -or @($acl.Access | Where-Object {
    $sid = $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
    $sid -ne $userSid -and $sid -ne 'S-1-5-18'
}).Count -ne 0) {
    throw 'Compose secret file ACL includes unexpected identities'
}

# The only output is a local path. The secret value is never printed.
Write-Output $target
