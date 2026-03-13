<#
.SYNOPSIS
    Synchronizes secrets from a .env file to GitHub Actions repository secrets.

.DESCRIPTION
    This script parses a .env file and uses the GitHub CLI (gh) to upload each key-value pair
    as a repository secret. It handles multiline values (like JSON keys) correctly.

.PARAMETER EnvFile
    The path to the .env file. Defaults to ".env" in the current directory.

.EXAMPLE
    .\scripts\sync_secrets.ps1
#>

param (
    [string]$EnvFile = ".env"
)

if (-not (Test-Path $EnvFile)) {
    Write-Error "Error: $EnvFile not found."
    exit 1
}

# Check if gh is installed or find it in common locations
$ghCommand = Get-Command gh -ErrorAction SilentlyContinue
if (-not $ghCommand) {
    $commonPaths = @(
        "C:\Program Files\GitHub CLI\gh.exe",
        "C:\Program Files (x86)\GitHub CLI\gh.exe",
        "$env:LOCALAPPDATA\Microsoft\WinGet\Packages\GitHub.cli_Microsoft.Winget.Source_8wekyb3d8bbwe\gh.exe"
    )
    foreach ($path in $commonPaths) {
        if (Test-Path $path) {
            $ghCommand = $path
            break
        }
    }
}

if (-not $ghCommand) {
    Write-Error "Error: GitHub CLI (gh) is not installed or not found. Please install it first: winget install --id GitHub.cli"
    exit 1
}

function gh { & $ghCommand $args }

# Check if logged in
gh auth status
if ($LASTEXITCODE -ne 0) {
    Write-Error "Error: Not logged into GitHub CLI. Run 'gh auth login' first."
    exit 1
}

Write-Host "Reading secrets from $EnvFile..." -ForegroundColor Cyan

$content = Get-Content $EnvFile -Raw
$lines = $content -split "`r?`n"
$currentKey = $null
$currentValue = @()

$secrets = @{}

foreach ($line in $lines) {
    # Match a new key-value pair. A key must be at the start of the line, 
    # all uppercase/numbers/underscores, and followed immediately by '='.
    if ($line -match "^(?<key>[A-Z0-9_]+)=(?<val>.*)") {
        if ($currentKey) {
            # Save previous secret
            $secrets[$currentKey] = ($currentValue -join "`n").Trim()
        }
        $currentKey = $Matches.key
        $val = $Matches.val
        
        # Check if the value is a single-line quoted value
        if ($val -match '^"(?<inner>.*)"$') {
            $secrets[$currentKey] = $Matches.inner.Replace('\"', '"')
            $currentKey = $null # Mark as done immediately
            $currentValue = @()
        } else {
            $currentValue = @($val)
        }
    } elseif ($currentKey) {
        # Continue current secret
        $currentValue += $line
    }
}
# Final secret
if ($currentKey) {
    $secrets[$currentKey] = ($currentValue -join "`n").Trim()
}

# Correctly handle multiline quoted values and sync
foreach ($key in $secrets.Keys) {
    $value = $secrets[$key]
    
    # Final cleanup for multiline quoted strings. 
    # Use (?s) to allow . to match newlines.
    if ($value -match '(?s)^"(?<inner>.*)"$') {
        $value = $Matches.inner.Replace('\"', '"')
    }

    Write-Host "Syncing secret: $key... ($( $value.Length ) bytes)" -ForegroundColor Yellow
    
    # Use --body - to read from stdin
    $value | gh secret set $key --body -
    
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Successfully synced $key" -ForegroundColor Green
    } else {
        Write-Host "Failed to sync $key" -ForegroundColor Red
    }
}

Write-Host "Finished syncing secrets." -ForegroundColor Cyan
