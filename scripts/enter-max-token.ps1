param(
    [switch]$Prepare,
    [switch]$Enter,
    [switch]$Check
)

$ErrorActionPreference = 'Stop'
$selectedModes = @($Prepare, $Enter, $Check) | Where-Object { $_ }
if (@($selectedModes).Count -ne 1) {
    throw 'Укажите ровно один режим: -Prepare, -Enter или -Check.'
}

$secretDirectory = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'MAXFleet\secrets'
$tokenPath = Join-Path $secretDirectory 'max_bot_token'
$currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User

function Set-PrivateAcl([string]$Path, [bool]$Directory) {
    $acl = Get-Acl -LiteralPath $Path
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) {
        $acl.RemoveAccessRuleSpecific($rule)
    }
    $inheritance = if ($Directory) { 'ContainerInherit,ObjectInherit' } else { 'None' }
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $currentSid, 'FullControl', $inheritance, 'None', 'Allow'
    )
    $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $Path -AclObject $acl
}

if ($Prepare -or $Enter) {
    New-Item -ItemType Directory -Path $secretDirectory -Force | Out-Null
    Set-PrivateAcl -Path $secretDirectory -Directory $true
}

if ($Enter) {
    $secureValue = Read-Host 'Вставьте токен MAX (ввод скрыт)' -AsSecureString
    if ($secureValue.Length -eq 0) { throw 'Пустой токен не сохраняется.' }
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureValue)
    try {
        $plainValue = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
        [IO.File]::WriteAllText($tokenPath, $plainValue, [Text.UTF8Encoding]::new($false))
    }
    finally {
        $plainValue = $null
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
    }
    Set-PrivateAcl -Path $tokenPath -Directory $false
}

if ($Check -or $Enter) {
    $present = (Test-Path -LiteralPath $tokenPath -PathType Leaf) -and
        ((Get-Item -LiteralPath $tokenPath).Length -gt 0)
    Write-Output "MAX_BOT_TOKEN_FILE присутствует: $present"
} else {
    Write-Output "Приватный каталог подготовлен: $(Test-Path -LiteralPath $secretDirectory -PathType Container)"
}
