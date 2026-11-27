# Install mcpsh on Windows.
#
#   irm https://raw.githubusercontent.com/davidemaggi/mcpsh/main/install.ps1 | iex
#
# Environment:
#   MCPSH_VERSION      version to install, e.g. 1.2.3 (default: the latest release)
#   MCPSH_INSTALL_DIR  where to put mcpsh.exe (default: %LOCALAPPDATA%\Programs\mcpsh)
#   MCPSH_BASE_URL     release download base (default: https://github.com/davidemaggi/mcpsh/releases)
#   MCPSH_NO_PATH=1    do not add the install directory to the user PATH
#
# The Authenticode-signed build is preferred when the release has one; every download is
# verified against the release checksums before anything is installed.
# Works in Windows PowerShell 5.1 and PowerShell 7+.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$repo = 'davidemaggi/mcpsh'
$baseUrl = if ($env:MCPSH_BASE_URL) { $env:MCPSH_BASE_URL } else { "https://github.com/$repo/releases" }
$installDir = if ($env:MCPSH_INSTALL_DIR) { $env:MCPSH_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\mcpsh' }

function Say($msg) { Write-Host "mcpsh-install: $msg" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "unsupported architecture $($env:PROCESSOR_ARCHITECTURE)" }
}

$version = $env:MCPSH_VERSION
if (-not $version) {
    $latest = Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$repo/releases/latest"
    $version = $latest.tag_name
}
$version = $version.TrimStart('v')
$url = "$baseUrl/download/v$version"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("mcpsh-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    function Get-Asset($name) {
        $out = Join-Path $tmp $name
        Invoke-WebRequest -UseBasicParsing -Uri "$url/$name" -OutFile $out
        return $out
    }
    function Get-Expected($sumsFile, $name) {
        foreach ($line in Get-Content $sumsFile) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $name) { return $parts[0].ToLower() }
        }
        return $null
    }

    # Prefer the signed build; fall back to the unsigned one when the release has none.
    $candidates = @(
        @{ Asset = "mcpsh_${version}_windows_${arch}-signed.zip"; Sums = 'checksums-signed.txt' },
        @{ Asset = "mcpsh_${version}_windows_${arch}.zip"; Sums = 'checksums.txt' }
    )
    $zip = $null
    foreach ($c in $candidates) {
        try {
            $sums = Get-Asset $c.Sums
            $file = Get-Asset $c.Asset
        } catch {
            continue
        }
        $expected = Get-Expected $sums $c.Asset
        if (-not $expected) { throw "$($c.Asset) is not listed in $($c.Sums)" }
        $actual = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLower()
        if ($actual -ne $expected) { throw "checksum mismatch for $($c.Asset) (expected $expected, got $actual)" }
        $zip = $file
        Say "downloaded $($c.Asset)"
        break
    }
    if (-not $zip) { throw "no Windows $arch build found for version $version at $url" }

    Expand-Archive -Path $zip -DestinationPath (Join-Path $tmp 'x') -Force
    $exe = Join-Path $tmp 'x\mcpsh.exe'
    if (-not (Test-Path $exe)) { throw 'the archive does not contain mcpsh.exe' }

    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    $target = Join-Path $installDir 'mcpsh.exe'
    $old = "$target.old"
    Remove-Item -Force -ErrorAction SilentlyContinue $old
    # A running mcpsh (the background daemon) cannot be overwritten, but it can be renamed;
    # the new CLI replaces the old daemon by itself.
    if (Test-Path $target) { Rename-Item -Path $target -NewName 'mcpsh.exe.old' }
    Copy-Item $exe $target
    Remove-Item -Force -ErrorAction SilentlyContinue $old

    if (-not $env:MCPSH_NO_PATH) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $parts = @($userPath -split ';' | Where-Object { $_ })
        if ($parts -notcontains $installDir) {
            [Environment]::SetEnvironmentVariable('Path', (($parts + $installDir) -join ';'), 'User')
            Say "added $installDir to your user PATH (open a new terminal to use it)"
        }
        if (($env:Path -split ';') -notcontains $installDir) { $env:Path = "$env:Path;$installDir" }
    }
    # Not running the new binary here on purpose: antivirus software can hold a freshly
    # copied executable for a long time on its first start. The checksum verified it.
    Say "installed mcpsh $version"
    Say "  -> $target"
    Say 'next: mcpsh add <name> -- <command>   and   mcpsh skill install'
} finally {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmp
}
