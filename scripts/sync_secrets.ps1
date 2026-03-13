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
# Regex to match KEY=VALUE pairs, including multiline values
# It looks for KEY= followed by either a quoted string (with potential newlines) or a single line value
$matches = [regex]::Matches($content, '(?m)^([A-Z0-9_]+)=(.*?(?:(?<=")(?:[^"\\]|\\.)*?"|.*$))')

foreach ($match in $matches) {
    $key = $match.Groups[1].Value
    $rawValue = $match.Groups[2].Value.Trim()

    # Determine the actual value (handle quotes and escaping)
    if ($rawValue -match '^"(.*)"$') {
        $value = $matches.Groups[1].Value.Replace('\"', '"')
    } else {
        $value = $rawValue
    }

    Write-Host "Syncing secret: $key..." -ForegroundColor Yellow
    
    # Use --body - to read from stdin, ensuring no interactive prompt
    # and avoiding command-line argument limits/escaping issues
    $value | gh secret set $key --body -
    
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Successfully synced $key" -ForegroundColor Green
    } else {
        Write-Host "Failed to sync $key" -ForegroundColor Red
    }
}

Write-Host "Finished syncing secrets." -ForegroundColor Cyan
