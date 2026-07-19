# GoModel installer for Windows.
#
#   irm https://gomodel.enterpilot.io/install.ps1 | iex
#
# Downloads the latest release from GitHub, verifies its SHA-256 checksum,
# and installs gomodel.exe to %LOCALAPPDATA%\Programs\gomodel (added to the
# user PATH when missing). No telemetry is sent by this script.
#
# Overrides (set before running):
#   $env:GOMODEL_VERSION      install a specific version (e.g. v0.1.50); default: latest
#   $env:GOMODEL_INSTALL_DIR  install directory; default: %LOCALAPPDATA%\Programs\gomodel

$ErrorActionPreference = 'Stop'

$Repo = 'ENTERPILOT/GoModel'
$Binary = 'gomodel'

# PROCESSOR_ARCHITEW6432 reports the real machine architecture when running
# in a 32-bit PowerShell on a 64-bit Windows.
$rawArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($rawArch) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "unsupported architecture: $rawArch" }
}

$tag = $env:GOMODEL_VERSION
if (-not $tag) {
    # Resolve the latest tag from the releases/latest redirect, avoiding the
    # GitHub API and its per-IP rate limit (same approach as install.sh).
    $req = [System.Net.WebRequest]::Create("https://github.com/$Repo/releases/latest")
    $req.AllowAutoRedirect = $false
    $res = $req.GetResponse()
    try {
        $location = $res.Headers['Location']
    }
    finally {
        $res.Close()
    }
    if (-not $location) { throw 'could not resolve the latest release' }
    $tag = ($location -split '/')[-1]
}
if ($tag -notmatch '^v') { throw "unexpected release tag: $tag" }
$version = $tag.TrimStart('v')

$archive = "${Binary}_${version}_windows_${arch}.zip"
$baseUrl = "https://github.com/$Repo/releases/download/$tag"

$tmpDir = Join-Path ([IO.Path]::GetTempPath()) "gomodel-install-$([IO.Path]::GetRandomFileName())"
New-Item -ItemType Directory -Path $tmpDir | Out-Null
try {
    Write-Host "Downloading $Binary $tag (windows/$arch)..."
    Invoke-WebRequest -Uri "$baseUrl/$archive" -OutFile (Join-Path $tmpDir $archive)
    Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile (Join-Path $tmpDir 'checksums.txt')

    $expected = $null
    foreach ($line in Get-Content (Join-Path $tmpDir 'checksums.txt')) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Length -ge 2 -and $parts[1] -eq $archive) { $expected = $parts[0]; break }
    }
    if (-not $expected) { throw "no checksum for $archive in checksums.txt" }
    $actual = (Get-FileHash (Join-Path $tmpDir $archive) -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) { throw "checksum mismatch for $archive" }
    Write-Host 'Checksum verified.'

    Expand-Archive -Path (Join-Path $tmpDir $archive) -DestinationPath $tmpDir -Force

    $installDir = $env:GOMODEL_INSTALL_DIR
    if (-not $installDir) { $installDir = Join-Path $env:LOCALAPPDATA 'Programs\gomodel' }
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item (Join-Path $tmpDir "$Binary.exe") (Join-Path $installDir "$Binary.exe") -Force

    Write-Host ''
    Write-Host "Installed $Binary $tag to $installDir\$Binary.exe"

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $installDir) {
        $newPath = if ([string]::IsNullOrEmpty($userPath)) { $installDir } else { "$userPath;$installDir" }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "Added $installDir to your user PATH - restart your terminal to pick it up."
    }

    Write-Host ''
    Write-Host 'Get started:'
    Write-Host '  $env:OPENAI_API_KEY = "sk-..."   # or any other provider key'
    Write-Host "  $Binary"
    Write-Host ''
    Write-Host 'Docs: https://gomodel.enterpilot.io'
}
finally {
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}
