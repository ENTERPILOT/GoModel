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

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$tag = $env:GOMODEL_VERSION
if (-not $tag) {
    $tag = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name
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

    $expected = (Get-Content (Join-Path $tmpDir 'checksums.txt') |
        Where-Object { $_ -match [regex]::Escape($archive) } |
        ForEach-Object { ($_ -split '\s+')[0] }) | Select-Object -First 1
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
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
        Write-Host "Added $installDir to your user PATH — restart your terminal to pick it up."
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
