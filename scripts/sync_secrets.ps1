<#
.SYNOPSIS
    Synchronizes secrets from a .env file to GitHub Actions repository secrets.

.DESCRIPTION
    This script parses a .env file and uses the GitHub CLI (gh) to upload each key-value pair
    as a repository secret. It handles multiline values (like JSON keys) correctly by writing
    each secret to a temporary file before uploading.

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

# Check if logged in
& $ghCommand auth status
if ($LASTEXITCODE -ne 0) {
    Write-Error "Error: Not logged into GitHub CLI. Run 'gh auth login' first."
    exit 1
}

Write-Host "Reading secrets from $EnvFile..." -ForegroundColor Cyan

$content = Get-Content $EnvFile -Raw
$lines = $content -split "`r?`n"
$currentKey = $null
$currentValue = @()

$secrets = [ordered]@{}

foreach ($line in $lines) {
    # Match a new key-value pair. Handles optional 'export ' prefix.
    # Keys must be ALL_CAPS_WITH_UNDERSCORES followed by '='.
    if ($line -match '^\s*(?:export\s+)?(?<key>[A-Z][A-Z0-9_]*)=(?<val>.*)') {
        if ($currentKey) {
            # Save previous secret
            $secrets[$currentKey] = ($currentValue -join "`n")
        }
        $currentKey = $Matches.key
        $val = $Matches.val
        $currentValue = @($val)
    } elseif ($currentKey) {
        # Continue current multiline secret
        $currentValue += $line
    }
}
# Save final secret
if ($currentKey) {
    $secrets[$currentKey] = ($currentValue -join "`n")
}

# Sync secrets using temp files (avoids all PowerShell pipe encoding issues)
foreach ($key in $secrets.Keys) {
    $value = $secrets[$key].Trim()
    
    Write-Host "`n--- $key ($($value.Length) bytes) ---" -ForegroundColor Yellow
    Write-Host $value -ForegroundColor Gray
    Write-Host "--- end $key ---" -ForegroundColor Yellow

    # Write to a temp file with UTF-8 no BOM encoding
    $tempFile = [System.IO.Path]::GetTempFileName()
    [System.IO.File]::WriteAllText($tempFile, $value, [System.Text.UTF8Encoding]::new($false))
    
    try {
        # Use file redirection instead of piping to avoid PowerShell encoding issues
        Get-Content $tempFile -Raw | & $ghCommand secret set $key
        
        if ($LASTEXITCODE -eq 0) {
            Write-Host "Successfully synced $key" -ForegroundColor Green
        } else {
            Write-Host "Failed to sync $key" -ForegroundColor Red
        }
    } finally {
        Remove-Item $tempFile -ErrorAction SilentlyContinue
    }
}

Write-Host "`nFinished syncing secrets." -ForegroundColor Cyan
