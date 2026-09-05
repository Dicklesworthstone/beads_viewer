#Requires -Version 5.1
<#
.SYNOPSIS
    Install beads_viewer (bv) on Windows from a checksum-verified release archive.
.DESCRIPTION
    Resolves a release tag (latest by default, or -Version), downloads the
    matching bv_<version>_windows_amd64.zip and the release's checksums.txt,
    verifies the archive's SHA-256 with Get-FileHash, and only then extracts
    bv.exe into the install directory. Any missing or mismatching checksum
    aborts the install: nothing unverified is ever written to the install
    directory. Go is not required.

    -FromSource keeps the old path (go install) but pins it to the resolved
    tag instead of @latest, so a build from source is still reproducible.
.PARAMETER Version
    Release tag to install, e.g. v0.23.0. Default: the latest GitHub release.
.PARAMETER InstallDir
    Where bv.exe is placed. Default: %LOCALAPPDATA%\Programs\bv.
.PARAMETER FromSource
    Build with `go install ...@<tag>` instead of downloading a release archive.
.PARAMETER NoPathUpdate
    Do not add the install directory to the user PATH.
.EXAMPLE
    # Pin the script to a commit rather than piping `main` (see README):
    irm https://raw.githubusercontent.com/Dicklesworthstone/beads_viewer/<commit>/install.ps1 -OutFile install.ps1
    .\install.ps1 -Version v0.23.0
.NOTES
    BV_INSTALL_API_URL and BV_INSTALL_DOWNLOAD_URL override the GitHub API and
    download bases; they exist so tests/scripts/install_ps1_test.sh can run the
    script against a local fake release. Leave them unset for real installs.
#>

[CmdletBinding()]
param(
    [string]$Version = "",
    [string]$InstallDir = "",
    [switch]$FromSource,
    [switch]$NoPathUpdate
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$REPO_OWNER = "Dicklesworthstone"
$REPO_NAME = "beads_viewer"
$MODULE = "github.com/$REPO_OWNER/$REPO_NAME"
$BIN_NAME = "bv"
$MIN_GO_VERSION = "1.25"

$apiBase = if ($env:BV_INSTALL_API_URL) { $env:BV_INSTALL_API_URL.TrimEnd('/') } else { "https://api.github.com/repos/$REPO_OWNER/$REPO_NAME" }
$downloadBase = if ($env:BV_INSTALL_DOWNLOAD_URL) { $env:BV_INSTALL_DOWNLOAD_URL.TrimEnd('/') } else { "https://github.com/$REPO_OWNER/$REPO_NAME/releases/download" }

function Write-Info { param([string]$Message) Write-Host "==> " -ForegroundColor Blue -NoNewline; Write-Host $Message }
function Write-Success { param([string]$Message) Write-Host "==> " -ForegroundColor Green -NoNewline; Write-Host $Message }
function Write-Warn { param([string]$Message) Write-Host "==> " -ForegroundColor Yellow -NoNewline; Write-Host $Message }
function Fail {
    param([string]$Message)
    Write-Host "==> " -ForegroundColor Red -NoNewline
    Write-Host $Message
    exit 1
}

# Windows PowerShell 5.1 has no $IsWindows; treat the Desktop edition as Windows.
function Test-IsWindowsHost {
    if (Get-Variable -Name IsWindows -ErrorAction SilentlyContinue) { return [bool]$IsWindows }
    return $true
}

function Get-DefaultInstallDir {
    if ($env:LOCALAPPDATA) { return Join-Path $env:LOCALAPPDATA "Programs\$BIN_NAME" }
    if ($env:USERPROFILE) { return Join-Path $env:USERPROFILE ".local\bin" }
    return Join-Path $HOME ".local/bin"
}

function Invoke-JsonGet {
    param([string]$Url)
    try {
        return Invoke-RestMethod -Uri $Url -Headers @{ 'User-Agent' = "$BIN_NAME-install.ps1"; 'Accept' = 'application/vnd.github+json' } -TimeoutSec 60
    } catch {
        Fail "Could not query $Url : $($_.Exception.Message)"
    }
}

function Resolve-ReleaseTag {
    param([string]$Requested)
    if ($Requested) {
        $tag = $Requested.Trim()
        if ($tag -notmatch '^v') { $tag = "v$tag" }
        if ($tag -notmatch '^v\d+\.\d+\.\d+([-.][0-9A-Za-z.]+)?$') { Fail "Version '$Requested' is not a release tag like v0.23.0" }
        return $tag
    }
    $latest = Invoke-JsonGet "$apiBase/releases/latest"
    if (-not $latest.tag_name) { Fail "The latest-release response carries no tag_name; pass -Version explicitly" }
    return [string]$latest.tag_name
}

function Get-ReleaseAssetNames {
    param([string]$Tag)
    $release = Invoke-JsonGet "$apiBase/releases/tags/$Tag"
    if (-not $release.assets) { return @() }
    return @($release.assets | ForEach-Object { [string]$_.name })
}

function Select-ArchiveName {
    param([string]$Tag, [string[]]$Names)
    $arch = "amd64"
    try {
        $osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
        if ($osArch -eq 'Arm64') { $arch = "arm64" }
    } catch { }
    if ($arch -ne "amd64") {
        Fail "No prebuilt Windows $arch release exists (goreleaser builds windows/amd64 only); use -FromSource with Go $MIN_GO_VERSION+"
    }
    $versioned = "${BIN_NAME}_$($Tag.TrimStart('v'))_windows_$arch.zip"
    $legacy = "${BIN_NAME}_windows_$arch.zip"
    foreach ($candidate in @($versioned, $legacy)) {
        if ($Names -contains $candidate) { return $candidate }
    }
    Fail "Release $Tag has neither $versioned nor $legacy among its assets: $($Names -join ', ')"
}

function Get-ExpectedChecksum {
    param([string]$ChecksumsPath, [string]$AssetName)
    foreach ($line in Get-Content -Path $ChecksumsPath) {
        # goreleaser writes "<sha256>  <name>"; accept one or more spaces and an optional '*'.
        if ($line -match '^([0-9A-Fa-f]{64})\s+\*?(\S+)\s*$' -and $Matches[2] -eq $AssetName) {
            return $Matches[1].ToLowerInvariant()
        }
    }
    return $null
}

function Assert-BinaryVersion {
    param([string]$Binary, [string]$Tag)
    # The portable fixture harness can check archive handling on Linux, but
    # executable identity must be checked by the native Windows harness.
    if (-not (Test-IsWindowsHost)) { return }
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $Binary
    $start.Arguments = '--version'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $start
    try {
        if (-not $process.Start()) { Fail "Could not run $Binary --version" }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(10000)) {
            $process.Kill()
            Fail "$Binary --version timed out; existing installation was not changed"
        }
        $reported = $stdout.GetAwaiter().GetResult().Trim()
        $diagnostic = $stderr.GetAwaiter().GetResult().Trim()
        if ($process.ExitCode -ne 0) {
            Fail "$Binary --version exited with code $($process.ExitCode): $diagnostic"
        }
        if ($reported.Length -gt 4096 -or $reported -notmatch '^bv\s+(v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)$') {
            Fail "Unexpected --version output from downloaded binary; existing installation was not changed"
        }
        if ($Matches[1].TrimStart('v') -cne $Tag.TrimStart('v')) {
            Fail "Downloaded binary reports $reported, expected $Tag; existing installation was not changed"
        }
    } finally {
        $process.Dispose()
    }
}

function Install-FromRelease {
    param([string]$Tag, [string]$TargetDir)

    $assetNames = Get-ReleaseAssetNames $Tag
    $assetName = Select-ArchiveName $Tag $assetNames
    if ($assetNames -notcontains "checksums.txt") { Fail "Release $Tag publishes no checksums.txt; refusing an unverified install" }

    $work = Join-Path ([System.IO.Path]::GetTempPath()) ("bv-install-" + [System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $work -Force | Out-Null
    try {
        $archivePath = Join-Path $work $assetName
        $checksumsPath = Join-Path $work "checksums.txt"
        $archiveUrl = "$downloadBase/$Tag/$assetName"
        $checksumsUrl = "$downloadBase/$Tag/checksums.txt"

        Write-Info "Downloading $assetName"
        try {
            Invoke-WebRequest -Uri $archiveUrl -OutFile $archivePath -TimeoutSec 300 -UseBasicParsing
            Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -TimeoutSec 60 -UseBasicParsing
        } catch {
            Fail "Download failed: $($_.Exception.Message)"
        }

        $expected = Get-ExpectedChecksum $checksumsPath $assetName
        if (-not $expected) { Fail "checksums.txt does not list $assetName; refusing to install" }
        $actual = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            Fail "SHA-256 mismatch for ${assetName}: expected $expected, got $actual. Refusing to install a tampered or corrupt archive."
        }
        Write-Info "SHA-256 verified against the release's checksums.txt"

        $extractDir = Join-Path $work "extract"
        Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force
        $exe = Get-ChildItem -Path $extractDir -Recurse -Filter "$BIN_NAME.exe" | Select-Object -First 1
        if (-not $exe) { Fail "$assetName does not contain $BIN_NAME.exe" }
        Assert-BinaryVersion $exe.FullName $Tag

        New-Item -ItemType Directory -Path $TargetDir -Force | Out-Null
        $destination = Join-Path $TargetDir "$BIN_NAME.exe"
        Copy-Item -Path $exe.FullName -Destination $destination -Force
        return $destination
    } finally {
        Remove-Item -Path $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Get-GoVersion {
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCmd) { return $null }
    $output = & go version 2>$null
    if ($output -match 'go(\d+\.\d+(?:\.\d+)?)') { return $Matches[1] }
    return $null
}

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

function Install-FromSource {
    param([string]$Tag, [string]$TargetDir)
    $goVersion = Get-GoVersion
    if (-not $goVersion) { Fail "Go is not installed or not in PATH (needed only for -FromSource). Install Go $MIN_GO_VERSION+ from https://go.dev/dl/" }
    if (-not (Test-GoVersion $goVersion $MIN_GO_VERSION)) { Fail "Go $MIN_GO_VERSION or later is required for -FromSource. Found: go$goVersion" }
    Write-Info "Building $BIN_NAME $Tag from source with Go $goVersion (pinned, not @latest)"
    $work = Join-Path ([System.IO.Path]::GetTempPath()) ("bv-build-" + [System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $work | Out-Null
    $previousCGO = $env:CGO_ENABLED
    $previousGOBIN = $env:GOBIN
    $prev = $ErrorActionPreference
    try {
        $env:CGO_ENABLED = "0"
        $env:GOBIN = $work
        $ErrorActionPreference = 'Continue'
        & go install "$MODULE/cmd/$BIN_NAME@$Tag" 2>&1 | ForEach-Object { Write-Host $_ }
        $ErrorActionPreference = $prev
        if ($LASTEXITCODE -ne 0) { Fail "go install exited with code $LASTEXITCODE" }
        $binary = Join-Path $work "$BIN_NAME.exe"
        Assert-BinaryVersion $binary $Tag
        New-Item -ItemType Directory -Path $TargetDir -Force | Out-Null
        $destination = Join-Path $TargetDir "$BIN_NAME.exe"
        Copy-Item -LiteralPath $binary -Destination $destination -Force
        return $destination
    } finally {
        $ErrorActionPreference = $prev
        $env:CGO_ENABLED = $previousCGO
        $env:GOBIN = $previousGOBIN
        Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Add-ToPathIfNeeded {
    param([string]$Dir)
    if (-not (Test-IsWindowsHost)) { return }
    $userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    $entries = if ($userPath) { $userPath -split ';' } else { @() }
    if ($entries | Where-Object { $_ -ieq $Dir } | Select-Object -First 1) { return }
    Write-Info "Adding $Dir to the user PATH"
    $newPath = if ($userPath) { "$userPath;$Dir" } else { $Dir }
    [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
    $env:PATH = "$env:PATH;$Dir"
    Write-Warn "Restart your terminal for the PATH change to take effect."
}

function Main {
    $tag = Resolve-ReleaseTag $Version
    $targetDir = if ($InstallDir) { $InstallDir } else { Get-DefaultInstallDir }
    Write-Info "Installing $BIN_NAME $tag into $targetDir"

    $binary = if ($FromSource) { Install-FromSource $tag $targetDir } else { Install-FromRelease $tag $targetDir }
    if (-not (Test-Path $binary)) { Fail "Installation failed: $binary is missing" }

    if (Test-IsWindowsHost) {
        $reported = & $binary --version 2>&1
        if ($LASTEXITCODE -ne 0) { Fail "$binary --version exited with code $LASTEXITCODE" }
        Write-Success "Installed $binary ($reported)"
    } else {
        # Only tests run this script off Windows; the .exe cannot execute here.
        Write-Success "Installed $binary (version check skipped: not a Windows host)"
    }

    if (-not $NoPathUpdate) { Add-ToPathIfNeeded $targetDir }
    Write-Info "Run '$BIN_NAME' in any beads project directory to view issues."
}

Main
