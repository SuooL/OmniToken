<#
.SYNOPSIS
  OmniToken agent one-click installer for Windows.

.DESCRIPTION
  Downloads the Windows agent, verifies it against SHA256SUMS, optionally enrolls
  the device, and registers a hidden logon scheduled task (OmniTokenAgent) that
  keeps the agent running. The Windows counterpart of scripts/install.sh.

.EXAMPLE
  # Enroll + install as a scheduled task (run in PowerShell):
  $env:OMNITOKEN_ADMIN_TOKEN='<ADMIN>'
  & ([scriptblock]::Create((irm https://ingest.example.net/agent/install.ps1))) `
      -Server https://ingest.example.net -Name $env:COMPUTERNAME `
      -BaseUrl https://ingest.example.net/agent

.NOTES
  Env: OMNITOKEN_ADMIN_TOKEN (enrollment credential), OMNITOKEN_BASE_URL, OMNITOKEN_DEVICE_TOKEN.
  Mesh/overlay (plaintext HTTP to a non-loopback hub): add -AllowInsecureHttp.
  Flaky DNS: add -ResolveIp <hub-ip> (ADR-0026 §3).
#>
[CmdletBinding()]
param(
  [string]$Server = "",
  [string]$Name = $env:COMPUTERNAME,
  [string]$ResolveIp = "",
  [switch]$AllowInsecureHttp,
  [string]$BaseUrl = $(if ($env:OMNITOKEN_BASE_URL) { $env:OMNITOKEN_BASE_URL } else { "https://github.com/SuooL/OmniToken/releases/latest/download" }),
  [string]$Prefix = "$env:USERPROFILE\.local\bin",
  [switch]$NoEnroll,
  [switch]$NoService
)

$ErrorActionPreference = "Stop"
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch {}
function Die($m) { Write-Error "install: $m"; exit 1 }

# --- architecture -> asset ---------------------------------------------------
switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { $asset = "omnitoken-windows-amd64.exe" }
  "ARM64" { $asset = "omnitoken-windows-amd64.exe"; Write-Host "note: no native arm64 build; using amd64 under emulation" }
  default { Die "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

# --- download + verify + install ---------------------------------------------
$tmp = Join-Path $env:TEMP ("omnitoken-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
  Write-Host "==> downloading $asset from $BaseUrl"
  $dl = Join-Path $tmp "omnitoken.exe"
  Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$asset" -OutFile $dl
  $sums = Join-Path $tmp "SHA256SUMS"
  Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/SHA256SUMS" -OutFile $sums
  $line = Select-String -Path $sums -Pattern ([regex]::Escape($asset)) | Select-Object -First 1
  if (-not $line) { Die "no checksum for $asset in SHA256SUMS" }
  $want = ($line.Line -split '\s+')[0]
  $got = (Get-FileHash -Algorithm SHA256 -Path $dl).Hash
  if ($want.ToLower() -ne $got.ToLower()) { Die "checksum mismatch for ${asset}: want $want got $got" }
  Write-Host "==> checksum ok"

  New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
  $bin = Join-Path $Prefix "omnitoken.exe"
  # Stop a running agent so the exe is not locked while we replace it.
  Get-Process omnitoken -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
  Start-Sleep -Milliseconds 500
  Copy-Item -Force $dl $bin
  Write-Host "==> installed $(& $bin version) to $bin"
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# --- config path -------------------------------------------------------------
$cfgDir = Join-Path $env:USERPROFILE ".omnitoken"
New-Item -ItemType Directory -Force -Path $cfgDir | Out-Null
$config = Join-Path $cfgDir "agent.json"

# --- enroll ------------------------------------------------------------------
if (-not $NoEnroll -and $Server) {
  if (-not $env:OMNITOKEN_ADMIN_TOKEN) { Die "OMNITOKEN_ADMIN_TOKEN is required to enroll (or pass -NoEnroll)" }
  $enrollArgs = @("agent", "enroll", "-config", $config, "-server", $Server)
  if ($Name) { $enrollArgs += @("-name", $Name) }
  if ($AllowInsecureHttp) { $enrollArgs += "-allow-insecure-http" }
  Write-Host "==> enrolling against $Server"
  & $bin @enrollArgs
  if ($LASTEXITCODE -ne 0) { Die "enrollment failed" }
  if ($ResolveIp) {
    $j = Get-Content $config -Raw | ConvertFrom-Json
    $j | Add-Member -NotePropertyName resolve_ip -NotePropertyValue $ResolveIp -Force
    [IO.File]::WriteAllText($config, ($j | ConvertTo-Json -Depth 30))
    Write-Host "==> pinned resolve_ip=$ResolveIp"
  }
} elseif (-not $NoEnroll -and -not $Server) {
  Write-Host "==> no -Server given; skipping enrollment (binary installed only)"
}

# --- service: hidden logon scheduled task ------------------------------------
if ($NoService) { Write-Host "==> -NoService: done"; exit 0 }
if (-not (Test-Path $config)) { Write-Host "==> no agent config; skipping service"; exit 0 }

# A .vbs launcher runs the agent with a hidden window — a scheduled-task action
# on a console exe would flash a console at every logon. The .bat prepends git's
# directory so the agent can attribute repositories (git is looked up on PATH).
$bat = Join-Path $cfgDir "omnitoken-agent.bat"
$vbs = Join-Path $cfgDir "omnitoken-agent-hidden.vbs"
$gitLine = ""
$git = Get-Command git -ErrorAction SilentlyContinue
if ($git) { $gitLine = "set PATH=%PATH%;" + (Split-Path $git.Source) }
@"
@echo off
$gitLine
"$bin" agent -config "$config"
"@ | Set-Content -Encoding OEM -Path $bat
@"
Set sh = CreateObject("Wscript.Shell")
sh.Run "cmd /c ""$bat""", 0, False
"@ | Set-Content -Encoding OEM -Path $vbs

Write-Host "==> registering scheduled task OmniTokenAgent (at logon, hidden)"
$action = New-ScheduledTaskAction -Execute "wscript.exe" -Argument ('"' + $vbs + '"')
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "OmniTokenAgent" -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName "OmniTokenAgent"
Start-Sleep -Seconds 4
if (Get-Process omnitoken -ErrorAction SilentlyContinue) {
  Write-Host "==> agent running"
} else {
  Write-Host "==> agent not detected yet; check Task Scheduler and $cfgDir\agent.log"
}
Write-Host "==> done."
