#Requires -Version 5.1
<#
.SYNOPSIS
    Install script for beads_viewer (bv) on Windows.
.DESCRIPTION
    Builds and installs bv from source using Go.
    Pre-built Windows binaries are not yet available, so Go 1.21+ is required.
.EXAMPLE
    irm https://raw.githubusercontent.com/Dicklesworthstone/beads_viewer/main/install.ps1 | iex
#>

[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$REPO = "github.com/Dicklesworthstone/beads_viewer"
$BIN_NAME = "bv"
$MIN_GO_VERSION = "1.21"

function Write-Info { param([string]$Message) Write-Host "==> " -ForegroundColor Blue -NoNewline; Write-Host $Message }
function Write-Success { param([string]$Message) Write-Host "==> " -ForegroundColor Green -NoNewline; Write-Host $Message }
function Write-Error2 { param([string]$Message) Write-Host "==> " -ForegroundColor Red -NoNewline; Write-Host $Message }
function Write-Warn { param([string]$Message) Write-Host "==> " -ForegroundColor Yellow -NoNewline; Write-Host $Message }

function Test-GoVersion {
    param([string]$Version, [string]$MinVersion)

    $v1 = $Version -split '\.' | ForEach-Object { [int]$_ }
    $v2 = $MinVersion -split '\.' | ForEach-Object { [int]$_ }
    $max = [Math]::Max($v1.Count, $v2.Count)

    for ($i = 0; $i -lt $max; $i++) {
        $a = if ($i -lt $v1.Count) { $v1[$i] } else { 0 }
        $b = if ($i -lt $v2.Count) { $v2[$i] } else { 0 }
        if ($a -gt $b) { return $true }
        if ($a -lt $b) { return $false }
    }
    return $true
}

function Get-GoVersion {
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCmd) { return $null }

    $output = & go version 2>$null
    if ($output -match 'go(\d+\.\d+(?:\.\d+)?)') {
        return $Matches[1]
    }
    return $null
}

function Get-InstallDir {
    # Use GOBIN if set (go install will create it if needed)
    if ($env:GOBIN) {
        return $env:GOBIN
    }

    # Use GOPATH/bin if GOPATH is set
    if ($env:GOPATH) {
        return Join-Path $env:GOPATH "bin"
    }

    # Default to ~/go/bin (go install will create it if needed)
    return Join-Path $env:USERPROFILE "go\bin"
}

function Add-ToPathIfNeeded {
    param([string]$Dir)

    $userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    # Use case-insensitive contains check (more robust than -like with wildcards)
    $pathEntries = if ($userPath) { $userPath -split ';' } else { @() }
    $alreadyInPath = $pathEntries | Where-Object { $_ -ieq $Dir } | Select-Object -First 1

    if (-not $alreadyInPath) {
        Write-Info "Adding $Dir to user PATH..."
        # Handle case where user PATH is null or empty
        $newPath = if ($userPath) { "$userPath;$Dir" } else { $Dir }
        [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
        $env:PATH = "$env:PATH;$Dir"
        Write-Warn "Restart your terminal for PATH changes to take effect."
    }
}

function Main {
    Write-Info "Installing $BIN_NAME for Windows..."

    # Check Go installation
    $goVersion = Get-GoVersion
    if (-not $goVersion) {
        Write-Error2 "Go is not installed or not in PATH."
        Write-Error2 ""
        Write-Error2 "Please install Go $MIN_GO_VERSION or later:"
        Write-Error2 "  - Download from: https://go.dev/dl/"
        Write-Error2 "  - Or via winget: winget install GoLang.Go"
        Write-Error2 "  - Or via scoop:  scoop install go"
        Write-Error2 "  - Or via choco:  choco install golang"
        Write-Error2 ""
        Write-Error2 "Then run this installer again."
        exit 1
    }

    if (-not (Test-GoVersion $goVersion $MIN_GO_VERSION)) {
        Write-Error2 "Go $MIN_GO_VERSION or later is required. Found: go$goVersion"
        Write-Error2 "Please upgrade Go and try again."
        exit 1
    }

    Write-Info "Using Go $goVersion"

    # Get install directory
    $installDir = Get-InstallDir
    Write-Info "Install directory: $installDir"

    # Build and install using go install
    Write-Info "Building $BIN_NAME from source..."

    $env:CGO_ENABLED = "0"
    $prevErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & go install "$REPO/cmd/$BIN_NAME@latest" 2>&1 | ForEach-Object { Write-Host $_ }
    $ErrorActionPreference = $prevErrorActionPreference
    if ($LASTEXITCODE -ne 0) {
        Write-Error2 "Failed to build ${BIN_NAME}: go install exited with code $LASTEXITCODE"
        exit 1
    }

    # Verify installation
    $binaryPath = Join-Path $installDir "$BIN_NAME.exe"
    if (-not (Test-Path $binaryPath)) {
        Write-Error2 "Installation failed - binary not found at $binaryPath"
        exit 1
    }

    Write-Success "Installed $BIN_NAME to $binaryPath"

    # Ensure install directory is in PATH
    Add-ToPathIfNeeded $installDir

    Write-Info ""
    Write-Info "Installation complete!"
    Write-Info "Run '$BIN_NAME' in any beads project directory to view issues."
    Write-Info ""
    Write-Info "Tip: For best display, use Windows Terminal with a Nerd Font."
}

# Run main
Main
