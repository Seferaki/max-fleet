$ErrorActionPreference = 'Stop'

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$source = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'MAXFleet\secrets\data_api_token'
if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    & (Join-Path $PSScriptRoot 'bootstrap.ps1') | Out-Null
}
if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw 'data_api_token is missing after bootstrap'
}

$directory = Join-Path $repositoryRoot '.local\compose-secrets'
$target = Join-Path $directory 'data_api_token'
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
