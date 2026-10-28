# Installs TeamSesh on Windows:
#   irm https://raw.githubusercontent.com/Simplero/teamsesh/main/install.ps1 | iex
# Set $env:TEAMSESH_NO_SERVICE = "1" to skip starting the background task.
# Running it again updates TeamSesh.
$ErrorActionPreference = "Stop"

$repo = "Simplero/teamsesh"
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$name = "teamsesh_windows_$arch.zip"
$base = "https://github.com/$repo/releases/latest/download"
$dir = Join-Path $env:LOCALAPPDATA "TeamSesh\bin"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("teamsesh-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $tmp, $dir | Out-Null

try {
    Write-Host "Downloading TeamSesh for windows/$arch..."
    Invoke-WebRequest -UseBasicParsing "$base/$name" -OutFile (Join-Path $tmp $name)
    Invoke-WebRequest -UseBasicParsing "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt")
    $want = (Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -match " $([regex]::Escape($name))$" }) -split " " | Select-Object -First 1
    if (-not $want) { throw "no checksum for $name" }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $name)).Hash.ToLower()
    if ($got -ne $want) { throw "checksum mismatch for $name" }

    Expand-Archive -Force (Join-Path $tmp $name) $tmp
    if (Get-ScheduledTask -TaskName "TeamSesh" -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName "TeamSesh" -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
    }
    Copy-Item -Force (Join-Path $tmp "teamsesh.exe") (Join-Path $dir "teamsesh.exe")
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not ($userPath -split ";" | Where-Object { $_ -eq $dir })) {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$dir", "User")
    $env:Path = "$env:Path;$dir"
    Write-Host "Added $dir to your PATH (open a new terminal to use it)."
}
$exe = Join-Path $dir "teamsesh.exe"
Write-Host "Installed $(& $exe version) to $exe"

if (-not (Get-Command tailscale -ErrorAction SilentlyContinue) -and -not (Test-Path "C:\Program Files\Tailscale\tailscale.exe")) {
    Write-Host ""
    Write-Host "TeamSesh needs Tailscale. Install it from https://tailscale.com/download and log in, then run:"
    Write-Host "  teamsesh service install"
    return
}

if ($env:TEAMSESH_NO_SERVICE -eq "1") {
    Write-Host "Skipped the background task. Start it with: teamsesh service install"
} else {
    & $exe service install
}

Write-Host ""
Write-Host "Next: add your teammates by their Tailscale login (they add you too):"
Write-Host "  teamsesh team add alice@example.com"
Write-Host "Then share a session with: teamsesh share --last"
