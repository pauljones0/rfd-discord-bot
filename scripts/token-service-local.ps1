# token-service-local.ps1 - Eternal launcher for local Carfax token service
#
# Single-window script: runs token-service.exe and cloudflared as hidden
# background processes, monitors them, and auto-restarts on crash.
# Chrome opens headed (visible) for reCAPTCHA trust, everything else is hidden.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\token-service-local.ps1
#
# To stop: Ctrl+C or close this window

$ErrorActionPreference = "Continue"
$Host.UI.RawUI.WindowTitle = "Carfax Token Service"

# -- Configuration ------------------------------------------------------------
$ServiceDir     = Split-Path -Parent $PSScriptRoot  # repo root
$TokenExe       = Join-Path $ServiceDir "token-service.exe"
$ChromeDataDir  = Join-Path $ServiceDir "carfax-chrome-data"
# Read secret from .env file in repo root (CARFAX_TOKEN_SERVICE_SECRET=...)
$envFile = Join-Path $ServiceDir ".env"
$Secret = ""
if (Test-Path $envFile) {
    Get-Content $envFile | ForEach-Object {
        if ($_ -match '^CARFAX_TOKEN_SERVICE_SECRET=(.+)$') { $Secret = $Matches[1].Trim('"', "'", ' ') }
    }
}
if (-not $Secret) {
    $Secret = $env:CARFAX_TOKEN_SERVICE_SECRET
}
if (-not $Secret) {
    Write-Host "ERROR: Set CARFAX_TOKEN_SERVICE_SECRET in .env or environment" -ForegroundColor Red
    exit 1
}
$Port           = "8081"
$GCPProject     = "may2025-01"
$GCPRegion      = "us-central1"
$CloudRunSvc    = "rfd-discord-bot"
$GHRepo         = "pauljones0/rfd-discord-bot"
$RestartDelay   = 10
$TunnelLog      = Join-Path $ServiceDir "cloudflared.log"
$TokenStdout    = Join-Path $ServiceDir "token-service-stdout.log"
$TokenStderr    = Join-Path $ServiceDir "token-service-stderr.log"
$CloudflaredExe = "C:\Program Files (x86)\cloudflared\cloudflared.exe"

if (-not (Test-Path $CloudflaredExe)) {
    $CloudflaredExe = (Get-Command cloudflared -ErrorAction SilentlyContinue).Source
    if (-not $CloudflaredExe) {
        Write-Host "ERROR: cloudflared not found. Install: winget install Cloudflare.cloudflared" -ForegroundColor Red
        exit 1
    }
}

# Always rebuild to pick up code changes
Write-Host "Building token-service.exe..." -ForegroundColor Yellow
Push-Location $ServiceDir
go build -o token-service.exe ./cmd/token-service 2>&1
Pop-Location
if (-not (Test-Path $TokenExe)) { Write-Host "Build failed" -ForegroundColor Red; exit 1 }

# Set env for token service (inherited by child processes)
$env:TOKEN_SERVICE_SECRET = $Secret
$env:TOKEN_SERVICE_PORT   = $Port
Remove-Item Env:\PROXY_URL -ErrorAction SilentlyContinue

Write-Host @"

  Carfax Token Service - Local
  Port $Port | No proxy | Auto-restart
  Ctrl+C to stop
  ----------------------------------------
"@ -ForegroundColor Cyan

# -- Helpers -------------------------------------------------------------------

function Stop-All {
    Get-Process -Name "token-service" -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Get-Process -Name "cloudflared" -ErrorAction SilentlyContinue |
        Where-Object { $_.StartTime -gt (Get-Date).AddHours(-12) } |
        Stop-Process -Force -ErrorAction SilentlyContinue
}

function Remove-StaleLocks {
    foreach ($f in @("SingletonLock", "SingletonSocket", "lockfile")) {
        $p = Join-Path $ChromeDataDir $f
        if (Test-Path $p) { Remove-Item $p -Force -ErrorAction SilentlyContinue }
    }
}

function Wait-ForHealth {
    param([int]$TimeoutSec = 90)
    $start = Get-Date
    while (((Get-Date) - $start).TotalSeconds -lt $TimeoutSec) {
        try {
            $r = Invoke-RestMethod -Uri "http://localhost:$Port/health" -TimeoutSec 3 -ErrorAction Stop
            if ($r.page_ready -eq $true) { return $true }
        } catch {}
        Start-Sleep -Seconds 2
    }
    return $false
}

function Get-TunnelUrl {
    $timeout = 30; $start = Get-Date
    while (((Get-Date) - $start).TotalSeconds -lt $timeout) {
        if (Test-Path $TunnelLog) {
            $content = Get-Content $TunnelLog -Raw -ErrorAction SilentlyContinue
            if ($content) {
                $m = [regex]::Matches($content, 'https://[a-z0-9][-a-z0-9]*\.trycloudflare\.com')
                # Skip api.trycloudflare.com, pick the actual tunnel URL
                foreach ($match in $m) {
                    if ($match.Value -ne "https://api.trycloudflare.com") {
                        return $match.Value
                    }
                }
            }
        }
        Start-Sleep -Milliseconds 500
    }
    return $null
}

function Update-CloudRunUrl {
    param([string]$Url)
    Write-Host "  Cloud Run -> $Url" -ForegroundColor Yellow
    & gcloud run services update $CloudRunSvc --region $GCPRegion --project $GCPProject `
        --update-env-vars "CARFAX_TOKEN_SERVICE_URL=$Url" --quiet 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) { Write-Host "  Cloud Run OK" -ForegroundColor Green }
    else { Write-Host "  Cloud Run update failed" -ForegroundColor Red }

    & gh secret set CARFAX_TOKEN_SERVICE_URL --body $Url --repo $GHRepo 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) { Write-Host "  GitHub secret OK" -ForegroundColor Green }
    else { Write-Host "  GitHub secret failed" -ForegroundColor Red }
}

# -- Cleanup on Ctrl+C --------------------------------------------------------
$null = Register-EngineEvent PowerShell.Exiting -Action {
    Stop-All
    Write-Host "`nStopped." -ForegroundColor Yellow
}

# -- Main loop -----------------------------------------------------------------
$lastTunnelUrl = ""

while ($true) {
    $ts = Get-Date -Format "HH:mm:ss"
    Write-Host "`n[$ts] Starting..." -ForegroundColor Cyan

    Stop-All
    Start-Sleep -Seconds 2
    Remove-StaleLocks

    # -- Token service: hidden window (Chrome itself stays visible) -------------
    # Start-Process with -WindowStyle Hidden hides the console window but
    # Chrome's GUI window still appears since it's a separate process.
    # Capture stdout/stderr so we can tail token service logs.
    if (Test-Path $TokenStdout) { Remove-Item $TokenStdout -Force }
    if (Test-Path $TokenStderr) { Remove-Item $TokenStderr -Force }
    $tokenProc = Start-Process -FilePath $TokenExe `
        -WorkingDirectory $ServiceDir `
        -WindowStyle Hidden `
        -PassThru `
        -RedirectStandardOutput $TokenStdout `
        -RedirectStandardError $TokenStderr

    Write-Host "  Token service PID $($tokenProc.Id) - waiting for reCAPTCHA..." -ForegroundColor White
    if (Wait-ForHealth -TimeoutSec 90) {
        Write-Host "  Ready" -ForegroundColor Green
    } else {
        Write-Host "  Health timeout (Chrome may still load)" -ForegroundColor Yellow
    }

    # -- Cloudflared: hidden, stderr -> log file --------------------------------
    if (Test-Path $TunnelLog) { Remove-Item $TunnelLog -Force }
    $tunnelProc = Start-Process -FilePath $CloudflaredExe `
        -ArgumentList "tunnel", "--url", "http://localhost:$Port", "--no-autoupdate" `
        -WindowStyle Hidden `
        -PassThru `
        -RedirectStandardError $TunnelLog

    $tunnelUrl = Get-TunnelUrl
    if ($tunnelUrl) {
        Write-Host "  Tunnel: $tunnelUrl" -ForegroundColor Green
        if ($tunnelUrl -ne $lastTunnelUrl) {
            Update-CloudRunUrl -Url $tunnelUrl
            $lastTunnelUrl = $tunnelUrl
        }
        # Verify
        Start-Sleep -Seconds 3
        try {
            $h = Invoke-RestMethod -Uri "$tunnelUrl/health" -TimeoutSec 10 -ErrorAction Stop
            Write-Host "  Verified: page_ready=$($h.page_ready)" -ForegroundColor Green
        } catch {
            Write-Host "  Tunnel verify failed (may still be connecting)" -ForegroundColor Yellow
        }
    } else {
        Write-Host "  No tunnel URL (check cloudflared.log)" -ForegroundColor Red
    }

    # -- Monitor ----------------------------------------------------------------
    Write-Host "  Running. Health check every 2 min. Token activity shown below." -ForegroundColor DarkGray
    $hcTimer = Get-Date
    $lastLogLine = 0  # track how many stderr lines we've shown

    while ($true) {
        Start-Sleep -Seconds 10

        # -- Show new token service log lines (stderr has slog output) ---------
        if (Test-Path $TokenStderr) {
            $lines = @(Get-Content $TokenStderr -ErrorAction SilentlyContinue)
            if ($lines.Count -gt $lastLogLine) {
                for ($i = $lastLogLine; $i -lt $lines.Count; $i++) {
                    $line = $lines[$i]
                    # Color-code by log level
                    if ($line -match '"level":"ERROR"' -or $line -match 'level=ERROR') {
                        Write-Host "  LOG: $line" -ForegroundColor Red
                    } elseif ($line -match '"level":"WARN"' -or $line -match 'level=WARN') {
                        Write-Host "  LOG: $line" -ForegroundColor Yellow
                    } elseif ($line -match 'Token request received' -or $line -match 'Token sent successfully') {
                        Write-Host "  LOG: $line" -ForegroundColor Green
                    } elseif ($line -match 'Token generation failed') {
                        Write-Host "  LOG: $line" -ForegroundColor Red
                    } else {
                        Write-Host "  LOG: $line" -ForegroundColor DarkGray
                    }
                }
                $lastLogLine = $lines.Count
            }
        }

        $tOk = $tokenProc -and -not $tokenProc.HasExited
        $cOk = $tunnelProc -and -not $tunnelProc.HasExited

        if (-not $tOk -or -not $cOk) {
            $who = if (-not $tOk) { "Token service" } else { "Tunnel" }
            Write-Host "  $(Get-Date -Format 'HH:mm:ss') $who died, restarting in ${RestartDelay}s..." -ForegroundColor Red
            Stop-All
            Start-Sleep -Seconds $RestartDelay
            break
        }

        if (((Get-Date) - $hcTimer).TotalMinutes -ge 2) {
            $hcTimer = Get-Date
            try {
                $h = Invoke-RestMethod -Uri "http://localhost:$Port/health" -TimeoutSec 5 -ErrorAction Stop
                if ($h.page_ready -ne $true) {
                    Write-Host "  $(Get-Date -Format 'HH:mm:ss') Page not ready, restarting..." -ForegroundColor Yellow
                    Stop-All; Start-Sleep -Seconds $RestartDelay; break
                }
                Write-Host "  $(Get-Date -Format 'HH:mm:ss') Healthy (page_ready=true)" -ForegroundColor DarkGray
            } catch {
                Write-Host "  $(Get-Date -Format 'HH:mm:ss') Health failed, restarting..." -ForegroundColor Yellow
                Stop-All; Start-Sleep -Seconds $RestartDelay; break
            }
        }
    }
}
