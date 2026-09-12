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

    -FromSource resolves the release tag to a Git commit and builds a clean
    checkout with its vendored dependencies. Git and Go are required. The
    source and build diagnostics are retained if installation fails.
.PARAMETER Version
    Release tag to install, e.g. v0.23.0. Default: the latest GitHub release.
.PARAMETER InstallDir
    Where bv.exe is placed. Default: %LOCALAPPDATA%\Programs\bv.
.PARAMETER FromSource
    Build the verified tagged checkout with -mod=vendor instead of downloading
    a release archive. This includes the checkout's local dependency repairs.
.PARAMETER NoPathUpdate
    Do not add the install directory to the user PATH.
.EXAMPLE
    # Pin the script to a commit rather than piping `main` (see README):
    irm https://raw.githubusercontent.com/Dicklesworthstone/beads_viewer/<commit>/install.ps1 -OutFile install.ps1
    .\install.ps1 -Version v0.23.0
.NOTES
    BV_INSTALL_API_URL, BV_INSTALL_DOWNLOAD_URL and BV_INSTALL_SOURCE_URL
    override the GitHub API, download and Git source locations for isolated
    installer tests. Leave them unset for official installs.
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
$MIN_GO_VERSION = "1.26"

$apiBase = if ($env:BV_INSTALL_API_URL) { $env:BV_INSTALL_API_URL.TrimEnd('/') } else { "https://api.github.com/repos/$REPO_OWNER/$REPO_NAME" }
$downloadBase = if ($env:BV_INSTALL_DOWNLOAD_URL) { $env:BV_INSTALL_DOWNLOAD_URL.TrimEnd('/') } else { "https://github.com/$REPO_OWNER/$REPO_NAME/releases/download" }
$sourceUrl = if ($env:BV_INSTALL_SOURCE_URL) { $env:BV_INSTALL_SOURCE_URL } else { "https://github.com/$REPO_OWNER/$REPO_NAME.git" }

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
        $execution = [System.Diagnostics.Stopwatch]::StartNew()
        $streams = @(
            @{ Name = 'stdout'; Reader = $process.StandardOutput },
            @{ Name = 'stderr'; Reader = $process.StandardError }
        )
        foreach ($stream in $streams) {
            $stream.Buffer = New-Object char[] 1024
            $stream.Text = New-Object System.Text.StringBuilder
            $stream.Done = $false
            $stream.Truncated = $false
            $stream.ReadFailed = $false
            $stream.Pending = $stream.Reader.ReadAsync($stream.Buffer, 0, $stream.Buffer.Length)
        }
        $drain = $null
        $timedOut = $false
        $killFailed = $false
        while ($true) {
            # Read both pipes while the child runs, retaining at most 4096
            # characters each. Keep draining excess output to avoid deadlock.
            foreach ($stream in $streams) {
                if (-not $stream.Done -and $stream.Pending.IsCompleted) {
                    try {
                        $count = $stream.Pending.GetAwaiter().GetResult()
                        if ($count -eq 0) {
                            $stream.Done = $true
                        } else {
                            $keep = [Math]::Min($count, 4096 - $stream.Text.Length)
                            [void]$stream.Text.Append($stream.Buffer, 0, $keep)
                            if ($keep -lt $count) { $stream.Truncated = $true }
                            $stream.Pending = $stream.Reader.ReadAsync($stream.Buffer, 0, $stream.Buffer.Length)
                        }
                    } catch {
                        $stream.ReadFailed = $true
                        $stream.Done = $true
                    }
                }
            }
            if ($null -eq $drain) {
                if ($process.HasExited) {
                    $drain = [System.Diagnostics.Stopwatch]::StartNew()
                } elseif ($execution.ElapsedMilliseconds -ge 10000) {
                    # Preserve the original child deadline. Exiting between
                    # HasExited and Kill is harmless; a failed kill is reported.
                    $timedOut = $true
                    try { $process.Kill() } catch {
                        if (-not $process.HasExited) { $killFailed = $true }
                    }
                    $drain = [System.Diagnostics.Stopwatch]::StartNew()
                }
            }
            if ($null -ne $drain) {
                # A descendant may inherit these pipes after the child exits.
                # Never await EOF indefinitely, including after a timeout kill.
                if (($streams[0].Done -and $streams[1].Done) -or $drain.ElapsedMilliseconds -ge 1000) { break }
                Start-Sleep -Milliseconds 10
            } else {
                $remaining = [Math]::Max(0, 10000 - $execution.ElapsedMilliseconds)
                [void]$process.WaitForExit([int][Math]::Min(20, $remaining))
            }
        }
        $reported = $streams[0].Text.ToString().Trim()
        $diagnostic = ($streams | ForEach-Object {
            $text = $_.Text.ToString().Trim()
            if ($_.Truncated) { $text += ' [truncated at 4096 characters]' }
            if (-not $_.Done) { $text += ' [incomplete: pipe still open]' }
            if ($_.ReadFailed) { $text += ' [incomplete: pipe read failed]' }
            "$($_.Name): $text"
        }) -join [Environment]::NewLine
        if ($timedOut) {
            if ($killFailed) { $diagnostic += "`nCould not terminate the version-check process" }
            Fail "$Binary --version timed out; existing installation was not changed`n$diagnostic"
        }
        if ($process.ExitCode -ne 0) {
            Fail "$Binary --version exited with code $($process.ExitCode):`n$diagnostic"
        }
        if (-not $streams[0].Done -or -not $streams[1].Done -or $streams[0].ReadFailed -or $streams[1].ReadFailed) {
            Fail "Could not read complete --version output; existing installation was not changed`n$diagnostic"
        }
        if ($streams[0].Truncated -or $reported -notmatch '^bv\s+(v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)$') {
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

    # Windows PowerShell 5.1 progress rendering can stall redirected downloads.
    # Keep this preference local to the download function, not the caller's shell.
    $ProgressPreference = 'SilentlyContinue'
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
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & go version 2>&1
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previousPreference }
    $versionText = $output -join "`n"
    Write-Info $versionText
    if ($code -eq 0 -and $versionText -match 'go(\d+\.\d+(?:\.\d+)?)') { return $Matches[1] }
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
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { Fail "Git is required for -FromSource to verify the tagged checkout" }
    if ($Tag -notmatch '^v\d+\.\d+\.\d+([-.][0-9A-Za-z.]+)?$') { Fail "Source version '$Tag' is not a release tag" }
    Write-Info "Building $BIN_NAME $Tag from its vendored source with Go $goVersion"
    $work = Join-Path ([System.IO.Path]::GetTempPath()) ("bv-build-" + [System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $work | Out-Null
    Write-Info "Preparing source build at $work"
    $source = Join-Path $work 'source'
    $installed = $false
    $savedEnv = @{}
    foreach ($key in @('CGO_ENABLED', 'GOFLAGS', 'GOWORK', 'GOOS', 'GOARCH')) {
        $savedEnv[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
    }
    $prev = $ErrorActionPreference
    try {
        # Resolve before fetching, then verify the fetched commit. A moved tag
        # cannot silently substitute different source between these operations.
        $ref = "refs/tags/$Tag"
        $remote = @(Invoke-SourceGit $work 'resolve' @('ls-remote', '--exit-code', $sourceUrl, $ref, "$ref^{}"))
        $commit = $null
        foreach ($name in @("$ref^{}", $ref)) {
            $matchesForRef = @($remote | Where-Object { $_ -match ('^([0-9a-f]{40})\s+' + [regex]::Escape($name) + '$') })
            if ($matchesForRef.Count -gt 1) { Fail "Source tag $Tag resolved ambiguously" }
            if ($matchesForRef.Count -eq 1) { $commit = ($matchesForRef[0] -split '\s+')[0]; break }
        }
        if (-not $commit) { Fail "Source tag $Tag has no verified Git object" }
        $null = Invoke-SourceGit $work 'init' @('-c', 'init.defaultBranch=main', 'init', $source)
        $null = Invoke-SourceGit $work 'fetch' @('-C', $source, 'fetch', '--depth=1', '--no-tags', $sourceUrl, "${ref}:${ref}")
        $null = Invoke-SourceGit $work 'checkout' @('-C', $source, 'checkout', '--detach', $ref)
        $head = @(Invoke-SourceGit $work 'revision' @('-C', $source, 'rev-parse', 'HEAD'))
        if ($head.Count -ne 1 -or $head[0] -cne $commit) { Fail "Fetched source differs from resolved tag $Tag ($commit); existing installation was not changed" }
        $status = @(Invoke-SourceGit $work 'status-before' @('-C', $source, 'status', '--porcelain=v1', '--untracked-files=all'))
        if ($status.Count) { Fail 'Source checkout is not clean; existing installation was not changed' }
        $vendorManifest = Join-Path $source 'vendor/modules.txt'
        $moduleFile = Join-Path $source 'go.mod'
        if (-not (Test-Path -LiteralPath $vendorManifest -PathType Leaf) -or -not (Test-Path -LiteralPath $moduleFile -PathType Leaf)) {
            Fail 'Tagged source must contain go.mod and vendor/modules.txt; existing installation was not changed'
        }
        if (-not (Select-String -LiteralPath $moduleFile -Pattern ('^module\s+' + [regex]::Escape($MODULE) + '\s*$') -CaseSensitive -Quiet)) {
            Fail 'Tagged source has an unexpected module identity; existing installation was not changed'
        }
        Write-Info "Source commit=$commit; vendor/modules.txt SHA256=$((Get-FileHash -LiteralPath $vendorManifest -Algorithm SHA256).Hash.ToLowerInvariant())"
        $env:CGO_ENABLED = "0"
        $env:GOFLAGS = ''
        $env:GOWORK = 'off'
        $env:GOOS = ''
        $env:GOARCH = ''
        $binary = Join-Path $work "$BIN_NAME.exe"
        Push-Location $source
        try {
            [IO.File]::WriteAllText((Join-Path $work 'build.log'), '')
            $ErrorActionPreference = 'Continue'
            & go build '-mod=vendor' '-buildvcs=true' '-ldflags' "-X $MODULE/pkg/version.version=$Tag" '-o' $binary "./cmd/$BIN_NAME" 2>&1 |
                Tee-Object -FilePath (Join-Path $work 'build.log') | ForEach-Object { Write-Host $_ }
            $code = $LASTEXITCODE
            $ErrorActionPreference = $prev
            if ($code -ne 0) { Fail "Source build exited with code $code; existing installation was not changed" }
        } finally { Pop-Location }
        if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { Fail 'Source build produced no executable; existing installation was not changed' }
        $status = @(Invoke-SourceGit $work 'status-after' @('-C', $source, 'status', '--porcelain=v1', '--untracked-files=all'))
        if ($status.Count) { Fail 'Source changed during the build; existing installation was not changed' }
        Assert-BinaryVersion $binary $Tag
        $ErrorActionPreference = 'Continue'
        $buildInfo = @(& go version -m $binary 2>&1)
        $code = $LASTEXITCODE
        $ErrorActionPreference = $prev
        $buildInfo | Tee-Object -FilePath (Join-Path $work 'build-info.log') | ForEach-Object { Write-Host $_ }
        if ($code -ne 0 -or -not ($buildInfo -cmatch ('^\s*path\s+' + [regex]::Escape("$MODULE/cmd/$BIN_NAME") + '$')) -or
            -not ($buildInfo -match ('^\s*build\s+vcs.revision=' + $commit + '$')) -or
            -not ($buildInfo -match '^\s*build\s+vcs.modified=false$')) {
            Fail 'Built executable does not identify the clean resolved source; existing installation was not changed'
        }
        Write-Info "Built executable SHA256=$((Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant())"
        New-Item -ItemType Directory -Path $TargetDir -Force | Out-Null
        $destination = Join-Path $TargetDir "$BIN_NAME.exe"
        Copy-Item -LiteralPath $binary -Destination $destination -Force
        $installed = $true
        return $destination
    } finally {
        $ErrorActionPreference = $prev
        foreach ($key in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($key, $savedEnv[$key], 'Process') }
        if ($installed) {
            Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
        } else {
            Write-Info "Failed source build and diagnostics retained at $work"
        }
    }
}

function Invoke-SourceGit {
    param([string]$Work, [string]$Step, [string[]]$Arguments)
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = @(& git '-c' 'core.autocrlf=false' @Arguments 2>&1)
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previousPreference }
    [IO.File]::WriteAllText((Join-Path $Work "$Step.log"), '')
    $output | Tee-Object -FilePath (Join-Path $Work "$Step.log") | ForEach-Object { Write-Host $_ }
    if ($code -ne 0) { Fail "Git $Step failed with code $code; existing installation was not changed" }
    return @($output | ForEach-Object { $_.ToString() })
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
