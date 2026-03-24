# register-tunnel-url.ps1
# Watches cloudflared output for the quick tunnel URL and registers it with the
# Cloud Run service via POST /register-token-service. This replaces the old
# approach of updating CARFAX_TOKEN_SERVICE_URL env var (which created a new
# Cloud Run revision on every tunnel restart).
#
# Usage:
#   .\scripts\register-tunnel-url.ps1
#
# Prerequisites:
#   - cloudflared.exe installed
#   - TOKEN_SERVICE_SECRET env var set
#   - CLOUD_RUN_URL env var set (e.g., https://rfd-discord-bot-xxxxx-uc.a.run.app)

$ErrorActionPreference = "Stop"

$secret = $env:TOKEN_SERVICE_SECRET
if (-not $secret) {
    Write-Error "TOKEN_SERVICE_SECRET environment variable is required"
    exit 1
}

$cloudRunURL = $env:CLOUD_RUN_URL
if (-not $cloudRunURL) {
    # Discover from gcloud
    $cloudRunURL = (gcloud run services describe rfd-discord-bot --region us-central1 --format "value(status.url)" --project may2025-01 2>$null)
    if (-not $cloudRunURL) {
        Write-Error "Could not determine Cloud Run URL. Set CLOUD_RUN_URL env var."
        exit 1
    }
}

Write-Host "Cloud Run URL: $cloudRunURL"
Write-Host "Starting cloudflared tunnel on localhost:8081..."

# Start cloudflared and capture stderr (where the URL appears)
$process = Start-Process -FilePath "C:\Program Files (x86)\cloudflared\cloudflared.exe" `
    -ArgumentList "tunnel", "--url", "http://localhost:8081", "--no-autoupdate" `
    -RedirectStandardError "$PSScriptRoot\..\cloudflared.log" `
    -PassThru -NoNewWindow

# Wait for the tunnel URL to appear in the log
$maxWait = 30
$waited = 0
$tunnelURL = $null

while ($waited -lt $maxWait) {
    Start-Sleep -Seconds 1
    $waited++

    if (Test-Path "$PSScriptRoot\..\cloudflared.log") {
        $content = Get-Content "$PSScriptRoot\..\cloudflared.log" -Raw
        if ($content -match "(https://[a-z0-9-]+\.trycloudflare\.com)") {
            $tunnelURL = $Matches[1]
            break
        }
        # Check for rate limit error
        if ($content -match "429 Too Many Requests") {
            Write-Warning "Cloudflare rate-limited the quick tunnel request. Wait a few minutes and try again."
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            exit 1
        }
    }
}

if (-not $tunnelURL) {
    Write-Error "Timed out waiting for tunnel URL (${maxWait}s)"
    Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    exit 1
}

Write-Host "Tunnel URL: $tunnelURL"

# Register with Cloud Run service
$body = @{ url = $tunnelURL } | ConvertTo-Json
$headers = @{
    "Authorization" = "Bearer $secret"
    "Content-Type"  = "application/json"
}

try {
    $response = Invoke-RestMethod -Uri "$cloudRunURL/register-token-service" `
        -Method POST -Body $body -Headers $headers
    Write-Host "Registered successfully: $($response.status)"
} catch {
    Write-Warning "Failed to register with Cloud Run: $_"
    Write-Warning "You can manually register later with:"
    Write-Warning "  curl -X POST $cloudRunURL/register-token-service -H 'Authorization: Bearer <secret>' -H 'Content-Type: application/json' -d '{`"url`": `"$tunnelURL`"}'"
}

Write-Host ""
Write-Host "Tunnel is running. Press Ctrl+C to stop."
Write-Host "Token service URL will auto-refresh from Firestore on each /process-facebook call."

# Wait for cloudflared to exit
try {
    Wait-Process -Id $process.Id
} catch {
    Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
}
