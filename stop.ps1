[CmdletBinding()]
param(
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$Exe = Join-Path $Root 'codebuddy-gateway.exe'
$PidFile = Join-Path $Root 'codebuddy-gateway.pid'

function Test-ExactGatewayProcess {
    param([System.Diagnostics.Process]$Process)
    if (-not $Process) { return $false }
    try {
        return $Process.Path -and ([IO.Path]::GetFullPath($Process.Path) -ieq [IO.Path]::GetFullPath($Exe))
    } catch {
        return $false
    }
}

$targets = @()
if (Test-Path -LiteralPath $PidFile) {
    $raw = (Get-Content -LiteralPath $PidFile -Raw).Trim()
    $pidValue = 0
    if ([int]::TryParse($raw, [ref]$pidValue) -and $pidValue -gt 0) {
        $process = Get-Process -Id $pidValue -ErrorAction SilentlyContinue
        if (Test-ExactGatewayProcess $process) { $targets += $process }
    }
}

if ($Force -or $targets.Count -eq 0) {
    $targets += @(Get-Process -Name 'codebuddy-gateway' -ErrorAction SilentlyContinue | Where-Object {
        Test-ExactGatewayProcess $_
    })
}

$targets = @($targets | Sort-Object Id -Unique)
if ($targets.Count -eq 0) {
    if (Test-Path -LiteralPath $PidFile) { Remove-Item -LiteralPath $PidFile -Force }
    Write-Host 'CodeBuddy2API 当前没有运行。' -ForegroundColor Yellow
    exit 0
}

foreach ($process in $targets) {
    Stop-Process -Id $process.Id -Force
    Write-Host "已停止 CodeBuddy2API，PID=$($process.Id)" -ForegroundColor Green
}

if (Test-Path -LiteralPath $PidFile) { Remove-Item -LiteralPath $PidFile -Force }
