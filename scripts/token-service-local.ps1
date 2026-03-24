# token-service-local.ps1 — Eternal launcher for local Carfax token service
#
# Runs the token service with a headed Chrome browser on this machine,
# exposes it via Cloudflare Tunnel, and auto-updates Cloud Run when the
# tunnel URL changes. Restarts everything automatically on crash.
#
# Usage (run from repo root):
#   powershell -ExecutionPolicy Bypass -File scripts\token-service-local.ps1
#
# To stop: close the PowerShell window or Ctrl+C

$ErrorActionPreference = "Continue"

# ── Configuration ──────────────────────────────────────────────────────────────
$ServiceDir     = Split-Path -Parent $PSScriptRoot  # repo root
$TokenExe       = Join-Path $ServiceDir "token-service.exe"
$ChromeDataDir  = Join-Path $ServiceDir "carfax-chrome-data"
$Secret         = "fjRLzCS03Bal0hmR5eqb73LtPvsaFFRi"
$Port           = "8081"
$GCPProject     = "may2025-01"
$GCPRegion      = "us-central1"
$CloudRunSvc    = "rfd-discord-bot"
$GHRepo         = "pauljones0/rfd-discord-bot"
$RestartDelay   = 10  # seconds between restart attempts
$CloudflaredExe = "C:\Program Files (x86)\cloudflared\cloudflared.exe"

# Fallback cloudflared path
if (-not (Test-Path $CloudflaredExe)) {
    $CloudflaredExe = (Get-Command cloudflared -ErrorAction SilentlyContinue).Source
    if (-not $CloudflaredExe) {
        Write-Host "ERROR: cloudflared not found. Install via: winget install Cloudflare.cloudflared" -ForegroundColor Red
        exit 1
    }
}

# Build token service if missing
if (-not (Test-Path $TokenExe)) {
    Write-Host "Building token-service.exe..." -ForegroundColor Yellow
    Push-Location $ServiceDir
    go build -o token-service.exe ./cmd/token-service
    Pop-Location
    if (-not (Test-Path $TokenExe)) {
        Write-Host "ERROR: Failed to build token-service.exe" -ForegroundColor Red
        exit 1
    }
}

Write-Host @"

  =====================================================
    Carfax Token Service - Local Launcher
    Chrome + Cloudflare Tunnel + Auto-Update
    Press Ctrl+C to stop
  =====================================================

"@ -ForegroundColor Cyan

# ── Helper: Extract tunnel URL from cloudflared output ─────────────────────────
function Get-TunnelUrl {
    param([string]$LogFile)

    $timeout = 30
    $start = Get-Date

    while (((Get-Date) - $start).TotalSeconds -lt $timeout) {
        if (Test-Path $LogFile) {
            $content = Get-Content $LogFile -Raw -ErrorAction SilentlyContinue
            # cloudflared prints the tunnel URL in a line like:
            #   INF +----------------------------+
            #   INF |  https://xxx-xxx.trycloudflare.com |
            #   INF +----------------------------+
            # Match the actual random subdomain URL, NOT api.trycloudflare.com
            $matches_found = [regex]::Matches($content, 'https://([a-z0-9]+-)+[a-z0-9]+\.trycloudflare\.com')
            if ($matches_found.Count -gt 0) {
                return $matches_found[0].Value
            }
        }
        Start-Sleep -Milliseconds 500
    }
    return $null
}

# ── Helper: Update Cloud Run env var ───────────────────────────────────────────
function Update-CloudRunUrl {
    param([string]$TunnelUrl)

    Write-Host "  Updating Cloud Run CARFAX_TOKEN_SERVICE_URL -> $TunnelUrl" -ForegroundColor Yellow

    & gcloud run services update $CloudRunSvc `
        --region $GCPRegion `
        --project $GCPProject `
        --update-env-vars "CARFAX_TOKEN_SERVICE_URL=$TunnelUrl" `
        --quiet 2>&1 | Out-Null

    if ($LASTEXITCODE -eq 0) {
        Write-Host "  Cloud Run updated successfully" -ForegroundColor Green
    } else {
        Write-Host "  WARNING: Failed to update Cloud Run" -ForegroundColor Red
    }

    # Also update GitHub secret so future deploys use the new URL
    Write-Host "  Updating GitHub secret..." -ForegroundColor Yellow
    & gh secret set CARFAX_TOKEN_SERVICE_URL --body $TunnelUrl --repo $GHRepo 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Host "  GitHub secret updated" -ForegroundColor Green
    } else {
        Write-Host "  WARNING: Failed to update GitHub secret" -ForegroundColor Red
    }
}

# ── Helper: Wait for token service health ──────────────────────────────────────
function Wait-ForHealth {
    param([int]$TimeoutSec = 90)
    $start = Get-Date
    while (((Get-Date) - $start).TotalSeconds -lt $TimeoutSec) {
        try {
            $resp = Invoke-RestMethod -Uri "http://localhost:$Port/health" -TimeoutSec 3 -ErrorAction Stop
            if ($resp.page_ready -eq $true) {
                return $true
            }
        } catch {}
        Start-Sleep -Seconds 2
    }
    return $false
}

# ── Helper: Remove stale Chrome locks ──────────────────────────────────────────
function Remove-StaleLocks {
    $lockFile = Join-Path $ChromeDataDir "SingletonLock"
    if (Test-Path $lockFile) {
        Remove-Item $lockFile -Force -ErrorAction SilentlyContinue
        Write-Host "  Removed stale Chrome lock" -ForegroundColor Yellow
    }
    $lockFile2 = Join-Path $ChromeDataDir "SingletonSocket"
    if (Test-Path $lockFile2) {
        Remove-Item $lockFile2 -Force -ErrorAction SilentlyContinue
    }
}

# ── Main loop ──────────────────────────────────────────────────────────────────
$lastTunnelUrl = ""

while ($true) {
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    Write-Host "`n[$timestamp] Starting token service + tunnel..." -ForegroundColor Cyan

    # Clean up stale processes
    Get-Process -Name "token-service" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    # Only kill cloudflared instances that we started (leave system ones alone)
    Get-Process -Name "cloudflared" -ErrorAction SilentlyContinue | Where-Object {
        $_.MainWindowTitle -like "*token*" -or $_.StartTime -gt (Get-Date).AddMinutes(-2)
    } | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2

    # Remove stale Chrome locks from previous crash
    Remove-StaleLocks

    # ── Start token service in its own window ──────────────────────────────────
    # Using a separate window avoids stdout redirection issues that break
    # Chrome's subprocess spawning. The window title helps identify it.
    Write-Host "  Starting token-service.exe (port $Port, no proxy)..." -ForegroundColor White

    $tokenProc = Start-Process -FilePath $TokenExe `
        -WorkingDirectory $ServiceDir `
        -PassThru `
        -ArgumentList "" `
        -EnvironmentVariables @{
        } `
        -WindowStyle Normal

    # Set env vars before launch — Start-Process inherits the current env
    # (we already set TOKEN_SERVICE_SECRET and PORT above in first iteration,
    #  but let's be explicit for restarts)
    $env:TOKEN_SERVICE_SECRET = $Secret
    $env:TOKEN_SERVICE_PORT = $Port
    Remove-Item Env:\PROXY_URL -ErrorAction SilentlyContinue

    # Re-launch with env vars properly set (Start-Process inherits current process env)
    if ($tokenProc) { Stop-Process $tokenProc -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Seconds 1

    $tokenProc = Start-Process -FilePath $TokenExe `
        -WorkingDirectory $ServiceDir `
        -PassThru

    # Wait for health (Chrome + reCAPTCHA init takes ~30-60s)
    Write-Host "  Waiting for Chrome + reCAPTCHA to initialize (up to 90s)..." -ForegroundColor White
    if (Wait-ForHealth -TimeoutSec 90) {
        Write-Host "  Token service is READY" -ForegroundColor Green
    } else {
        Write-Host "  WARNING: Health check timed out (Chrome may still be loading)" -ForegroundColor Yellow
    }

    # ── Start cloudflared tunnel ───────────────────────────────────────────────
    $tunnelLog = Join-Path $ServiceDir "cloudflared.log"
    if (Test-Path $tunnelLog) { Remove-Item $tunnelLog -Force }

    Write-Host "  Starting cloudflared tunnel..." -ForegroundColor White

    # cloudflared writes its info to stderr. We redirect stderr to a log file
    # using cmd.exe wrapper to avoid PowerShell redirection issues.
    $tunnelProc = Start-Process -FilePath "cmd.exe" `
        -ArgumentList "/c", "`"$CloudflaredExe`" tunnel --url http://localhost:$Port --no-autoupdate 2>`"$tunnelLog`"" `
        -PassThru `
        -WindowStyle Minimized

    # Extract tunnel URL
    $tunnelUrl = Get-TunnelUrl -LogFile $tunnelLog
    if ($tunnelUrl) {
        Write-Host "  Tunnel URL: $tunnelUrl" -ForegroundColor Green

        # Update Cloud Run if URL changed
        if ($tunnelUrl -ne $lastTunnelUrl) {
            Update-CloudRunUrl -TunnelUrl $tunnelUrl
            $lastTunnelUrl = $tunnelUrl
        } else {
            Write-Host "  Tunnel URL unchanged, skipping update" -ForegroundColor Gray
        }

        # Quick verification
        Write-Host "  Verifying tunnel..." -ForegroundColor White
        Start-Sleep -Seconds 3  # give tunnel a moment to stabilize
        try {
            $healthResp = Invoke-RestMethod -Uri "$tunnelUrl/health" -TimeoutSec 10 -ErrorAction Stop
            Write-Host "  Tunnel OK: page_ready=$($healthResp.page_ready)" -ForegroundColor Green
        } catch {
            Write-Host "  WARNING: Tunnel verification failed (may still be connecting)" -ForegroundColor Yellow
        }
    } else {
        Write-Host "  WARNING: Could not get tunnel URL within 30s" -ForegroundColor Red
        Write-Host "  Check cloudflared.log for errors" -ForegroundColor Red
    }

    # ── Monitor both processes ─────────────────────────────────────────────────
    Write-Host "`n  Running. Monitoring every 30s... (Ctrl+C to stop)" -ForegroundColor Gray
    $healthCheckTimer = Get-Date

    while ($true) {
        Start-Sleep -Seconds 10

        $tokenAlive = $tokenProc -and -not $tokenProc.HasExited
        $tunnelAlive = $tunnelProc -and -not $tunnelProc.HasExited

        if (-not $tokenAlive -or -not $tunnelAlive) {
            $which = if (-not $tokenAlive) { "Token service" } else { "Cloudflared tunnel" }
            $timestamp = Get-Date -Format "HH:mm:ss"
            Write-Host "`n  [$timestamp] $which died! Restarting in ${RestartDelay}s..." -ForegroundColor Red

            # Kill the survivor
            if ($tokenAlive) { Stop-Process $tokenProc -Force -ErrorAction SilentlyContinue }
            if ($tunnelAlive) { Stop-Process $tunnelProc -Force -ErrorAction SilentlyContinue }

            Start-Sleep -Seconds $RestartDelay
            break  # restart outer loop
        }

        # Health check every 2 minutes
        if (((Get-Date) - $healthCheckTimer).TotalMinutes -ge 2) {
            $healthCheckTimer = Get-Date
            try {
                $health = Invoke-RestMethod -Uri "http://localhost:$Port/health" -TimeoutSec 5 -ErrorAction Stop
                if ($health.page_ready -ne $true) {
                    Write-Host "  Page not ready, restarting..." -ForegroundColor Yellow
                    Stop-Process $tokenProc -Force -ErrorAction SilentlyContinue
                    Stop-Process $tunnelProc -Force -ErrorAction SilentlyContinue
                    Start-Sleep -Seconds $RestartDelay
                    break
                }
                $timestamp = Get-Date -Format "HH:mm:ss"
                Write-Host "  [$timestamp] Health OK" -ForegroundColor DarkGray
            } catch {
                Write-Host "  Health check failed: $_ — restarting..." -ForegroundColor Yellow
                Stop-Process $tokenProc -Force -ErrorAction SilentlyContinue
                Stop-Process $tunnelProc -Force -ErrorAction SilentlyContinue
                Start-Sleep -Seconds $RestartDelay
                break
            }
        }
    }
}
