# Installs the hindcast binary from GitHub releases.
#
#   irm https://raw.githubusercontent.com/DushaneW/Hindcast/main/install.ps1 | iex
#
# HINDCAST_VERSION      release tag to install (default: latest)
# HINDCAST_INSTALL_DIR  target directory (default: %LOCALAPPDATA%\Programs\hindcast)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'DushaneW/Hindcast'
$version = if ($env:HINDCAST_VERSION) { $env:HINDCAST_VERSION } else { 'latest' }
$installDir = if ($env:HINDCAST_INSTALL_DIR) { $env:HINDCAST_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\hindcast' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "hindcast install: unsupported architecture $env:PROCESSOR_ARCHITECTURE" }
}

$base = if ($env:HINDCAST_BASE_URL) { $env:HINDCAST_BASE_URL }
        elseif ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" }
        else { "https://github.com/$repo/releases/download/$version" }

$name = "hindcast_windows_$arch"
$archive = "$name.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Write-Host "downloading $archive ($version)"
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($archive))$" }
    if (-not $line) { throw "hindcast install: $archive is not listed in checksums.txt" }
    $want = ($line -split ' ')[0]
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash.ToLower()
    if ($got -ne $want) { throw "hindcast install: checksum mismatch for $archive" }

    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item (Join-Path $tmp "$name\hindcast.exe") (Join-Path $installDir 'hindcast.exe') -Force
}
finally {
    Remove-Item -Recurse -Force $tmp
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $installDir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Write-Host "added $installDir to your PATH; open a new terminal to use it"
}
$installed = & (Join-Path $installDir 'hindcast.exe') version
Write-Host "installed hindcast $installed to $installDir"
