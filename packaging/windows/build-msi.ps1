<#
.SYNOPSIS
  Builds the Woodpile MSI with WiX v5.

.DESCRIPTION
  Stages woodpile.exe, adds the WiX Util and Firewall extensions, and runs `wix build`
  on woodpile.wxs. Without -Binary it builds woodpile.exe with `go build` first (build
  the frontend before that, so the real UI is embedded).

  Needs the WiX v5 .NET tool:  dotnet tool install --global wix --version 5.0.2

.EXAMPLE
  ./packaging/windows/build-msi.ps1 -Version v1.2.3 -Arch x64 -Binary dist/windows/amd64/woodpile.exe

.EXAMPLE
  ./packaging/windows/build-msi.ps1 -Version 1.2.3 -Arch arm64
#>
[CmdletBinding()]
param(
    # Release version; a leading "v" and any -prerelease/+build suffix are dropped for the
    # MSI ProductVersion, which must be numeric (major.minor.patch).
    [string]$Version = "",
    [ValidateSet("x64", "arm64")]
    [string]$Arch = "x64",
    # A prebuilt woodpile.exe for -Arch. Built with `go build` when omitted.
    [string]$Binary = "",
    # A prebuilt woodpile-aclfix.exe (./packaging/windows/aclfix) for -Arch. Built when omitted.
    [string]$AclFix = "",
    [string]$OutDir = "",
    [string]$ProjectUrl = "https://github.com/gmccollu/woodpile",
    [string]$WixVersion = "5.0.2"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Invoke-Native {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Exe failed with exit code $LASTEXITCODE" }
}

$here = $PSScriptRoot
$repo = (Resolve-Path (Join-Path $here "..\..")).Path
if (-not $OutDir) { $OutDir = Join-Path $repo "dist\msi" }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$OutDir = (Resolve-Path $OutDir).Path

if (-not $Version) {
    $Version = (git -C $repo describe --tags --abbrev=0 2>$null)
    if (-not $Version) { $Version = "0.0.0" }
}
$fullVersion = $Version.TrimStart("v")
$msiVersion = ($fullVersion -split "[-+]")[0]
if ($msiVersion -notmatch '^\d+\.\d+\.\d+$') {
    throw "Version '$Version' isn't major.minor.patch; the MSI needs a numeric version."
}
$parts = $msiVersion.Split(".") | ForEach-Object { [int]$_ }
if ($parts[0] -gt 255 -or $parts[1] -gt 255 -or $parts[2] -gt 65535) {
    throw "Version '$msiVersion' is out of range for an MSI (255.255.65535)."
}

$goarch = @{ x64 = "amd64"; arm64 = "arm64" }[$Arch]
$stage = Join-Path $OutDir "stage-$goarch"
New-Item -ItemType Directory -Force -Path $stage | Out-Null

if ($Binary) {
    Copy-Item -Force $Binary (Join-Path $stage "woodpile.exe")
} else {
    $commit = (git -C $repo rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = "none" }
    Write-Host "==> go build windows/$goarch ($fullVersion, $commit)"
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = $goarch
    try {
        Invoke-Native go @("-C", $repo, "build", "-trimpath",
            "-ldflags", "-s -w -X main.version=$fullVersion -X main.commit=$commit",
            "-o", (Join-Path $stage "woodpile.exe"), "./cmd/woodpile")
    } finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
}

# The data-folder ACL reset the MSI runs as a custom action (see woodpile.wxs).
if ($AclFix) {
    Copy-Item -Force $AclFix (Join-Path $stage "woodpile-aclfix.exe")
} else {
    Write-Host "==> go build woodpile-aclfix windows/$goarch"
    $env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = $goarch
    try {
        Invoke-Native go @("-C", $repo, "build", "-trimpath", "-ldflags", "-s -w",
            "-o", (Join-Path $stage "woodpile-aclfix.exe"), "./packaging/windows/aclfix")
    } finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
}

# The MSI also installs the license and the third-party notices next to the program.
Copy-Item -Force (Join-Path $repo "LICENSE") (Join-Path $stage "LICENSE.txt")
$notices = Join-Path $repo "THIRD_PARTY_NOTICES.txt"
if (-not (Test-Path $notices)) { throw "THIRD_PARTY_NOTICES.txt is missing; run scripts/third_party_notices.py" }
Copy-Item -Force $notices (Join-Path $stage "THIRD_PARTY_NOTICES.txt")

if (-not (Get-Command wix -ErrorAction SilentlyContinue)) {
    $tools = Join-Path $env:USERPROFILE ".dotnet\tools"
    if (Test-Path (Join-Path $tools "wix.exe")) { $env:PATH = "$tools;$env:PATH" }
    else { throw "wix not found. Install it with: dotnet tool install --global wix --version $WixVersion" }
}
$wixInstalled = (wix --version) -replace '\+.*$', ''
if (-not $wixInstalled.StartsWith("5.")) {
    throw "WiX $wixInstalled found; this script targets WiX v5 ($WixVersion)."
}

Write-Host "==> wix extensions"
Invoke-Native wix @("extension", "add", "-g", "WixToolset.Util.wixext/$wixInstalled", "WixToolset.Firewall.wixext/$wixInstalled")

$msi = Join-Path $OutDir "woodpile_${fullVersion}_windows_${goarch}.msi"
Write-Host "==> wix build $msi"
Invoke-Native wix @("build",
    "-arch", $Arch,
    "-ext", "WixToolset.Util.wixext",
    "-ext", "WixToolset.Firewall.wixext",
    "-d", "Version=$msiVersion",
    "-d", "BinDir=$stage",
    "-d", "ProjectUrl=$ProjectUrl",
    "-o", $msi,
    (Join-Path $here "woodpile.wxs"))

Remove-Item -Recurse -Force $stage
Write-Host "==> $msi"
