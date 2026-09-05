#Requires -Version 5.1
<#
Native Windows release and installer verification. Requires network access and
two published releases; all installations and evidence stay in a fresh temp
directory. The user PATH and shell profiles are never changed. The loopback
server supplies deliberately broken releases only, not the live positive proof.

  powershell.exe -NoProfile -File tests/scripts/install_native_test.ps1
  powershell.exe -NoProfile -File tests/scripts/install_native_test.ps1 -InstallerPath C:\old\install.ps1

Retains logs, downloaded checksums, fixture archives, and executables for review.
#>
[CmdletBinding()]
param(
    [string]$InstallerPath = (Join-Path $PSScriptRoot '..\..\install.ps1'),
    [string]$Version = 'v0.23.0',
    [string]$PreviousVersion = 'v0.22.0',
    [string]$WorkDir = '',
    [switch]$IncludeSource
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ((Get-Variable IsWindows -ErrorAction SilentlyContinue) -and -not $IsWindows) {
    throw 'This harness requires native Windows; Linux PowerShell is not native proof.'
}
if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -ne 'X64') {
    throw 'This harness requires native Windows x64, matching the published archive.'
}
$InstallerPath = (Resolve-Path -LiteralPath $InstallerPath).Path
if (-not $WorkDir) {
    $WorkDir = Join-Path ([IO.Path]::GetTempPath()) ('bv native tests ' + [Guid]::NewGuid().ToString('N'))
}
if (Test-Path -LiteralPath $WorkDir) { throw "WorkDir must be new: $WorkDir" }
New-Item -ItemType Directory -Path $WorkDir | Out-Null
$WorkDir = (Resolve-Path -LiteralPath $WorkDir).Path
$userPathBefore = [Environment]::GetEnvironmentVariable('PATH', 'User')
$savedEnv = @{}
foreach ($key in @('BV_INSTALL_API_URL', 'BV_INSTALL_DOWNLOAD_URL', 'BV_NO_BROWSER', 'BV_TEST_MODE', 'BV_NO_UPDATE_CHECK')) {
    $savedEnv[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
$env:BV_NO_BROWSER = '1'
$env:BV_TEST_MODE = '1'
$env:BV_NO_UPDATE_CHECK = '1'
$env:BV_INSTALL_API_URL = ''
$env:BV_INSTALL_DOWNLOAD_URL = ''
$server = $null
$failures = @()
$utf8 = New-Object Text.UTF8Encoding($false)

function Write-Utf8 {
    param([string]$Path, [string]$Value)
    [IO.File]::WriteAllText($Path, $Value, $utf8)
}
function Invoke-Logged {
    param([string]$Name, [string]$Binary, [string[]]$Argv, [switch]$AllowFailure)
    $log = Join-Path $WorkDir "$Name.log"
    Write-Host "RUN $Name : $Binary $($Argv -join ' ')"
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $output = & $Binary @Argv 2>&1
    $code = $LASTEXITCODE
    $ErrorActionPreference = $previousPreference
    Write-Utf8 $log (($output | Out-String) + "`nexit=$code`n")
    Write-Host "exit=$code; evidence=$log"
    if ($code -ne 0 -and -not $AllowFailure) { throw "$Name failed: $output" }
    return @{ ExitCode = $code; Output = ($output | Out-String).Trim() }
}
function Invoke-Installer {
    param([string]$Name, [string]$Tag, [string]$Target, [switch]$AllowFailure, [switch]$FromSource)
    $arguments = @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $InstallerPath, '-Version', $Tag, '-InstallDir', $Target, '-NoPathUpdate')
    if ($FromSource) { $arguments += '-FromSource' }
    return Invoke-Logged $Name 'powershell.exe' $arguments -AllowFailure:$AllowFailure
}
function Assert-Version {
    param([string]$Name, [string]$Binary, [string]$Tag)
    $result = Invoke-Logged "$Name-version" $Binary @('--version')
    if ($result.Output -cne "bv $Tag") { throw "Expected bv $Tag, got $($result.Output)" }
    $caps = Invoke-Logged "$Name-capabilities" $Binary @('--robot-capabilities')
    $manifest = $caps.Output | ConvertFrom-Json
    if ($manifest.version.TrimStart('v') -cne $Tag.TrimStart('v')) { throw 'Capabilities version differs from executable version' }
    if (@($manifest.commands | Where-Object { $_.name -eq 'robot-next' }).Count -ne 1) { throw 'Capabilities omit robot-next' }
}

try {
    Write-Host "Native evidence retained at $WorkDir"
    Write-Host "OS=$([Environment]::OSVersion.VersionString); OSArchitecture=$([Runtime.InteropServices.RuntimeInformation]::OSArchitecture); ProcessArchitecture=$([Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture); PowerShell=$($PSVersionTable.PSVersion)"
    Write-Host "Installer SHA256=$((Get-FileHash $InstallerPath -Algorithm SHA256).Hash)"
    $liveDir = Join-Path $WorkDir 'live install with spaces'
    $liveBinary = Join-Path $liveDir 'bv.exe'
    $null = Invoke-Installer 'live-install-previous' $PreviousVersion $liveDir
    Assert-Version 'previous' $liveBinary $PreviousVersion
    $oldBinary = Join-Path $WorkDir 'previous.exe'
    Copy-Item -LiteralPath $liveBinary -Destination $oldBinary
    $null = Invoke-Installer 'live-install-current' $Version $liveDir
    Assert-Version 'current' $liveBinary $Version
    $goodHash = (Get-FileHash $liveBinary -Algorithm SHA256).Hash
    Write-Host "Previous binary SHA256=$((Get-FileHash $oldBinary -Algorithm SHA256).Hash); current binary SHA256=$goodHash"
    if (Get-Command go -ErrorAction SilentlyContinue) {
        $null = Invoke-Logged 'current-build-info' 'go' @('version', '-m', $liveBinary)
        $null = Invoke-Logged 'previous-build-info' 'go' @('version', '-m', $oldBinary)
    }

    # Real JSONL loading and graph selection through an absolute Windows path
    # containing spaces; no shell shim or canned robot response is involved.
    $project = Join-Path $WorkDir 'tiny project with spaces'
    $beads = Join-Path $project '.beads'
    New-Item -ItemType Directory -Path $beads | Out-Null
    Write-Utf8 (Join-Path $beads 'issues.jsonl') (@(
        '{"id":"native-ready","title":"Native ready task","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}',
        '{"id":"native-dependent","title":"Wait for predecessor","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","dependencies":[{"issue_id":"native-dependent","depends_on_id":"native-ready","type":"blocks"}]}'
    ) -join "`n")
    Push-Location $project
    try {
        $next = Invoke-Logged 'tiny-project-next' $liveBinary @('--robot-next')
        if (($next.Output | ConvertFrom-Json).id -cne 'native-ready') { throw 'Tiny project selected an unexpected issue' }
    } finally { Pop-Location }

    # Exercise the real released self-updater, then the no-update path, without
    # touching whichever bv is installed in the user's PATH.
    $updateDir = Join-Path $WorkDir 'self update with spaces'
    New-Item -ItemType Directory -Path $updateDir | Out-Null
    $updateBinary = Join-Path $updateDir 'bv.exe'
    Copy-Item -LiteralPath $oldBinary -Destination $updateBinary
    $null = Invoke-Logged 'live-self-update' $updateBinary @('--update', '--yes')
    Assert-Version 'self-updated' $updateBinary $Version
    if ((Get-FileHash $updateBinary -Algorithm SHA256).Hash -cne $goodHash) { throw 'Self-update bytes differ from the freshly installed release' }
    $null = Invoke-Logged 'live-no-update' $updateBinary @('--update', '--yes')
    if ((Get-FileHash $updateBinary -Algorithm SHA256).Hash -cne $goodHash) { throw 'No-update changed the installed binary' }
    if ($IncludeSource) {
        if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw '-IncludeSource requires a real Go toolchain' }
        $sourceDir = Join-Path $WorkDir 'live source install with spaces'
        $null = Invoke-Installer 'live-source-install' $Version $sourceDir -FromSource
        Assert-Version 'source-installed' (Join-Path $sourceDir 'bv.exe') $Version
    }

    # Controlled compiler output tests the source installer's promotion boundary.
    # The shim copies a real older executable; this is not a live source build.
    $compiler = Join-Path $WorkDir 'controlled compiler fixture'
    New-Item -ItemType Directory -Path $compiler | Out-Null
    $compilerScript = @'
@echo off
if "%~1" == "version" (
  echo go version go1.25.5 windows/amd64
  exit /b 0
)
copy /Y "%BV_TEST_SOURCE_BINARY%" "%GOBIN%\bv.exe"
exit /b %errorlevel%
'@
    [IO.File]::WriteAllText((Join-Path $compiler 'go.cmd'), $compilerScript, [Text.Encoding]::ASCII)
    $sourceTarget = Join-Path $WorkDir 'wrong source version existing install'
    New-Item -ItemType Directory -Path $sourceTarget | Out-Null
    $sourceBinary = Join-Path $sourceTarget 'bv.exe'
    Copy-Item -LiteralPath $liveBinary -Destination $sourceBinary
    $previousPath = $env:PATH
    $previousSourceBinary = $env:BV_TEST_SOURCE_BINARY
    try {
        $env:PATH = "$compiler;$previousPath"
        $env:BV_TEST_SOURCE_BINARY = $oldBinary
        $result = Invoke-Installer 'negative-controlled-source-version' $Version $sourceTarget -FromSource -AllowFailure
    } finally {
        $env:PATH = $previousPath
        $env:BV_TEST_SOURCE_BINARY = $previousSourceBinary
    }
    $unchanged = (Get-FileHash $sourceBinary -Algorithm SHA256).Hash -ceq $goodHash
    if ($result.ExitCode -eq 0 -or -not $unchanged) {
        $failures += "controlled source version expected refusal and preserved install; exit=$($result.ExitCode), preserved=$unchanged"
    } else {
        Assert-Version 'preserved-source-version' $sourceBinary $Version
        Write-Host 'PASS controlled wrong-version source output refused and existing installation preserved'
    }

    # The negative fixtures deliberately lie about archive identity. Their
    # executables came from the two real, verified native installations above.
    $site = Join-Path $WorkDir 'negative fixtures'
    $asset = "bv_$($Version.TrimStart('v'))_windows_amd64.zip"
    foreach ($name in @('wrong-version', 'corrupt-checksum', 'corrupt-archive', 'missing-checksums')) {
        $api = Join-Path $site "$name\api\releases\tags"
        $dl = Join-Path $site "$name\dl\$Version"
        New-Item -ItemType Directory -Path $api, $dl -Force | Out-Null
        $names = @(@{ name = $asset })
        if ($name -ne 'missing-checksums') { $names += @{ name = 'checksums.txt' } }
        Write-Utf8 (Join-Path $api $Version) (@{ tag_name = $Version; assets = $names } | ConvertTo-Json -Depth 5)
        $payload = Join-Path $WorkDir "$name payload"
        New-Item -ItemType Directory -Path $payload | Out-Null
        $source = if ($name -eq 'wrong-version') { $oldBinary } else { $liveBinary }
        Copy-Item -LiteralPath $source -Destination (Join-Path $payload 'bv.exe')
        $archive = Join-Path $dl $asset
        if ($name -eq 'corrupt-archive') { Write-Utf8 $archive 'This is not a zip archive.' }
        else { Compress-Archive -LiteralPath (Join-Path $payload 'bv.exe') -DestinationPath $archive }
        $hash = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($name -eq 'corrupt-checksum') { $hash = '0' * 64 }
        Write-Utf8 (Join-Path $dl 'checksums.txt') "$hash  $asset`n"
    }

    # TcpListener avoids global URL reservations and requires no administrator
    # privileges. It only serves files in this disposable test fixture tree.
    $portFile = Join-Path $WorkDir 'fixture-port.txt'
    $server = Start-Job -ArgumentList $site, $portFile -ScriptBlock {
        param($site, $portFile)
        $ErrorActionPreference = 'Stop'
        $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
        $listener.Start()
        [IO.File]::WriteAllText($portFile, [string]$listener.LocalEndpoint.Port)
        try {
            while ($true) {
                # Keep the job stoppable while idle; a synchronous Accept call
                # otherwise prevents Stop-Job from completing in PowerShell 5.1.
                if (-not $listener.Pending()) { Start-Sleep -Milliseconds 50; continue }
                $client = $listener.AcceptTcpClient()
                try {
                    $stream = $client.GetStream()
                    $reader = New-Object IO.StreamReader($stream)
                    $request = $reader.ReadLine()
                    while ($reader.ReadLine()) { }
                    $relative = ([Uri]::UnescapeDataString(($request -split ' ')[1])).TrimStart('/')
                    $path = [IO.Path]::GetFullPath((Join-Path $site $relative))
                    if (-not $path.StartsWith($site + [IO.Path]::DirectorySeparatorChar) -or -not [IO.File]::Exists($path)) {
                        $body = [Text.Encoding]::UTF8.GetBytes('fixture not found')
                        $status = '404 Not Found'
                    } else {
                        $body = [IO.File]::ReadAllBytes($path)
                        $status = '200 OK'
                    }
                    $contentType = if ($relative -match '/api/') { 'application/json' } else { 'application/octet-stream' }
                    $header = [Text.Encoding]::ASCII.GetBytes("HTTP/1.1 $status`r`nContent-Length: $($body.Length)`r`nContent-Type: $contentType`r`nConnection: close`r`n`r`n")
                    $stream.Write($header, 0, $header.Length)
                    $stream.Write($body, 0, $body.Length)
                    $stream.Flush()
                } finally { $client.Close() }
            }
        } finally { $listener.Stop() }
    }
    for ($attempt = 0; $attempt -lt 100 -and -not (Test-Path $portFile); $attempt++) { Start-Sleep -Milliseconds 100 }
    if (-not (Test-Path $portFile)) { throw "Fixture server did not start: $(Receive-Job $server)" }
    $base = 'http://127.0.0.1:' + [IO.File]::ReadAllText($portFile)
    foreach ($name in @('wrong-version', 'corrupt-checksum', 'corrupt-archive', 'missing-checksums')) {
        $target = Join-Path $WorkDir "$name existing install"
        New-Item -ItemType Directory -Path $target | Out-Null
        $binary = Join-Path $target 'bv.exe'
        Copy-Item -LiteralPath $liveBinary -Destination $binary
        $env:BV_INSTALL_API_URL = "$base/$name/api"
        $env:BV_INSTALL_DOWNLOAD_URL = "$base/$name/dl"
        $result = Invoke-Installer "negative-$name" $Version $target -AllowFailure
        $unchanged = (Get-FileHash $binary -Algorithm SHA256).Hash -ceq $goodHash
        if ($result.ExitCode -eq 0 -or -not $unchanged) {
            $failures += "$name expected refusal and preserved install; exit=$($result.ExitCode), preserved=$unchanged"
        } else {
            Assert-Version "preserved-$name" $binary $Version
            Write-Host "PASS $name refused and existing installation preserved"
        }
    }
    if ($failures.Count) { throw ($failures -join "`n") }
    Write-Host 'PASS native Windows install, capabilities, tiny project, update/no-update, and failed-install preservation'
} finally {
    if ($server) {
        Stop-Job $server
        Receive-Job $server | Out-String | Write-Host
    }
    foreach ($key in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($key, $savedEnv[$key], 'Process') }
    if ([Environment]::GetEnvironmentVariable('PATH', 'User') -cne $userPathBefore) { throw 'Native test changed the user PATH' }
    Write-Host "Evidence retained at $WorkDir; user PATH unchanged"
}
