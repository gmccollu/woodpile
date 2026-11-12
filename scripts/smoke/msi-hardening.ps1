<#
.SYNOPSIS
  MSI hardening checks on a disposable Windows CI machine (installs Woodshed for real,
  as an administrator; never run it on a machine you care about).

.DESCRIPTION
  Before installing, it plants what a non-administrator could leave in C:\ProgramData:
  a Woodshed folder granting Everyone full control, a child with its own Everyone grant,
  a junction to a folder outside, and a hard link to a file outside. Then it installs the
  MSI and checks:
    - the data folder: owner Administrators, protected DACL with only SYSTEM, Administrators
      and NT SERVICE\Woodshed (modify);
    - every child: owner Administrators, only inherited ACEs (no Everyone);
    - the junction and the hard link are gone, and their targets and ACLs are untouched;
    - the service's required privileges are SeChangeNotifyPrivilege only (no
      SeImpersonatePrivilege), and it runs;
  then uninstalls with DELETE_LOGS=1. Then (security review N6) it plants a data folder
  owned by BUILTIN\Users and checks the install moves it aside to Woodshed.untrusted-*,
  contents kept (under a name with a random part, W3), and makes a fresh protected one;
  and that a fresh install creates the folder already protected (W2).

.EXAMPLE
  ./scripts/smoke/msi-hardening.ps1 -Msi dist/msi/woodshed_0.0.0_windows_amd64.msi
#>
param(
    [Parameter(Mandatory = $true)][string]$Msi,
    [string]$Logs = "$env:RUNNER_TEMP\smoke-logs"
)
$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force $Logs | Out-Null
$Msi = (Resolve-Path $Msi).Path
$Data = "C:\ProgramData\Woodshed"
# On the same volume as C:\ProgramData: a hard link can't cross drives (RUNNER_TEMP is D: on some runners).
$Outside = Join-Path $env:SystemDrive "woodshed-hardening-outside"
$ServiceSid = "S-1-5-80-246480601-2135328064-241538422-929878615-662582935"
$WantDacl = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;$ServiceSid)"  # "D:PAI(" counts too
$passes = 0

function Ok($m) { $script:passes++; Write-Host "PASS $m" }
function Die($m) { Write-Host "FAIL $m"; exit 1 }
function Msiexec([string[]]$a, [string]$log) {
    $p = Start-Process msiexec.exe -Wait -PassThru -ArgumentList ($a + @("/qn", "/l*v", "`"$log`""))
    if ($p.ExitCode -ne 0) { Copy-Item $log $Logs -ErrorAction SilentlyContinue; Die "msiexec $($a -join ' ') exited $($p.ExitCode)" }
}
# The planted log's path and text: the .log, or the service's .log.gz of it if the UTC day
# ended during the run (the .gz is the service's own file, so its ACL isn't checked).
function Planted-Log {
    if (Test-Path -LiteralPath $planted) { return @{ Path = $planted; Text = (Get-Content -LiteralPath $planted -Raw); Gz = $false } }
    $gz = "$planted.gz"
    if (-not (Test-Path -LiteralPath $gz)) { Die "the planted log $planted is gone (no .log or .log.gz)" }
    $f = [IO.File]::OpenRead($gz)
    try {
        $r = New-Object IO.StreamReader (New-Object IO.Compression.GZipStream ($f, [IO.Compression.CompressionMode]::Decompress))
        $text = $r.ReadToEnd()
    } finally { $f.Dispose() }
    Write-Host "the UTC day ended during the run: the service compressed $planted"
    return @{ Path = $gz; Text = $text; Gz = $true }
}
function DaclOf([string]$path) { ("D:" + ((Get-Acl -LiteralPath $path).Sddl -split "D:", 2)[1]) -replace "^D:PAI\(", "D:P(" }

if (Test-Path $Data) { Die "$Data exists before the test" }

# Installed file times must be the commit's UTC time on a machine that isn't on UTC (an MSI
# cabinet only holds zone-less local times). CI runners are on UTC, so switch for the test.
$origTz = (Get-TimeZone).Id
Set-TimeZone -Id "Eastern Standard Time"

Write-Host "==> plant a hostile data folder"
# The planted log is today's file (UTC, as the store names them), computed once. A file for
# an earlier day is a closed one: the service gzips it as soon as it starts, so a fixed date
# broke this check after midnight UTC. If midnight passes during the run, the service
# compresses this one too; Planted-Log below then reads the .gz instead.
$day = [DateTime]::UtcNow.ToString("yyyy-MM-dd")
$planted = "$Data\logs\10.0.0.1\$day.log"
New-Item -ItemType Directory -Force "$Data\logs\10.0.0.1", $Outside | Out-Null
Set-Content $planted "kept"
Set-Content (Join-Path $Outside "secret.txt") "outside"
icacls $Data /grant "*S-1-1-0:(OI)(CI)F" | Out-Null
icacls "$Data\logs" /grant "*S-1-1-0:F" | Out-Null
cmd /c mklink /J "$Data\evil" $Outside | Out-Null
cmd /c mklink /H "$Data\woodshed.toml" (Join-Path $Outside "secret.txt") | Out-Null
if (-not (Test-Path "$Data\evil") -or -not (Test-Path "$Data\woodshed.toml")) { Die "couldn't plant the junction and the hard link" }
$outsideBefore = (Get-Acl $Outside).Sddl
$secretBefore = (Get-Acl (Join-Path $Outside "secret.txt")).Sddl

Write-Host "==> install $Msi"
Msiexec @("/i", "`"$Msi`"") (Join-Path $Logs "hardening-install.log")

$sd = Get-Acl $Data
Write-Host "data folder: $($sd.Sddl)"
if ($sd.Owner -ne "BUILTIN\Administrators") { Die "data folder owner is $($sd.Owner)" }
if ((DaclOf $Data) -ne $WantDacl) { Die "data folder DACL is $(DaclOf $Data), want $WantDacl" }
Ok "data folder: owner Administrators, exactly the protected DACL"

# What was there before the install: reset to owner Administrators and inherited ACEs only.
# (Files the service creates once it runs are owned by its own SID, which is fine.)
$bad = @()
$plantedLog = Planted-Log
$check = @("$Data\logs", "$Data\logs\10.0.0.1")
if (-not $plantedLog.Gz) { $check += $plantedLog.Path }
foreach ($p in $check) {
    $a = Get-Acl -LiteralPath $p
    if ($a.Owner -ne "BUILTIN\Administrators" -or @($a.Access | Where-Object { -not $_.IsInherited }).Count -ne 0 -or $a.AreAccessRulesProtected) {
        $bad += "${p}: $($a.Owner) $($a.Sddl)"
    }
}
Get-ChildItem -LiteralPath $Data -Recurse -Force | ForEach-Object {
    $a = Get-Acl -LiteralPath $_.FullName
    if ($a.Sddl -match ";;;WD\)") { $bad += "$($_.FullName) still grants Everyone: $($a.Sddl)" }
}
if ($bad.Count) { Die "children not reset:`n$($bad -join "`n")" }
Ok "existing children: owner Administrators, inherited ACEs only; nothing grants Everyone"

if (Test-Path "$Data\evil") { Die "the junction is still there" }
if (Test-Path "$Data\woodshed.toml") {
    if ((Get-Content "$Data\woodshed.toml" -Raw) -match "outside") { Die "the hard link is still there" }
}
if ((Get-Content (Join-Path $Outside "secret.txt") -Raw) -notmatch "outside") { Die "the hard link's target was changed" }
if ((Get-Acl $Outside).Sddl -ne $outsideBefore) { Die "the junction's target ACL changed" }
if ((Get-Acl (Join-Path $Outside "secret.txt")).Sddl -ne $secretBefore) { Die "the hard link's target ACL changed" }
if ((Planted-Log).Text -notmatch "kept") { Die "existing logs were lost" }
Ok "junction and hard link removed; their targets untouched; existing logs kept"

$privs = (sc.exe qprivs Woodshed) -join "`n"
Write-Host $privs
if ($privs -notmatch "SeChangeNotifyPrivilege" -or $privs -match "SeImpersonatePrivilege") { Die "required privileges: $privs" }
Ok "required privileges: SeChangeNotifyPrivilege only"

for ($i = 0; $i -lt 30 -and (Get-Service Woodshed).Status -ne "Running"; $i++) { Start-Sleep 1 }
if ((Get-Service Woodshed).Status -ne "Running") { Die "the service isn't running" }
Ok "the service runs with them"

$want = [DateTimeOffset]::FromUnixTimeSeconds([long](git log -1 --format=%ct)).UtcDateTime
$got = (Get-Item (Join-Path $env:ProgramFiles "Woodshed\woodshed.exe")).LastWriteTimeUtc
if ([math]::Abs(($got - $want).TotalSeconds) -gt 2) { Die "woodshed.exe's time is $($got.ToString('u')), want the commit's $($want.ToString('u')) (time zone $((Get-TimeZone).Id))" }
Ok "installed woodshed.exe has the commit's time, $($want.ToString('u')), on a UTC-5 machine"

# The Start-menu shortcut opens the UI, with the woodshed icon.
$lnk = Join-Path $env:ProgramData "Microsoft\Windows\Start Menu\Programs\Woodshed.url"
if (-not (Test-Path $lnk)) { Die "no Start-menu shortcut at $lnk" }
$url = Get-Content $lnk -Raw
Write-Host $url
if ($url -notmatch "URL=http://127\.0\.0\.1:8514/") { Die "the shortcut doesn't open the UI" }
$icon = [regex]::Match($url, "IconFile=(.+)").Groups[1].Value.Trim()
if (-not $icon -or -not $icon.EndsWith("woodshed.ico") -or -not (Test-Path $icon)) { Die "the shortcut's icon isn't the installed woodshed.ico: '$icon'" }
Ok "Start-menu shortcut opens http://127.0.0.1:8514/ with the woodshed icon ($icon)"

Write-Host "==> uninstall with DELETE_LOGS=1"
Msiexec @("/x", "`"$Msi`"", "DELETE_LOGS=1") (Join-Path $Logs "hardening-uninstall.log")
if (Test-Path $Data) { Die "$Data is still there after DELETE_LOGS=1" }
if (-not (Test-Path (Join-Path $Outside "secret.txt"))) { Die "uninstall removed a file outside the data folder" }
Ok "uninstall removed the data folder and nothing outside it"

Write-Host "==> N6: a data folder an ordinary user created (owned by BUILTIN\Users)"
New-Item -ItemType Directory -Force "$Data\logs\10.0.0.2" | Out-Null
Set-Content "$Data\logs\10.0.0.2\$day.log" "from before"
icacls $Data /setowner "*S-1-5-32-545" | Out-Null
if ((Get-Acl $Data).Owner -ne "BUILTIN\Users") { Die "couldn't make BUILTIN\Users the owner" }
Msiexec @("/i", "`"$Msi`"") (Join-Path $Logs "hardening-install-untrusted.log")
$aside = @(Get-ChildItem C:\ProgramData -Directory -Filter "Woodshed.untrusted-*")
if ($aside.Count -ne 1) { Die "expected one Woodshed.untrusted-* folder, found $($aside.Count)" }
# W3: the name has a random part, so nobody can take it in advance to block the install.
if ($aside[0].Name -notmatch '^Woodshed\.untrusted-\d{8}T\d{6}Z-[0-9a-f]{8}$') { Die "set-aside name $($aside[0].Name) has no random part" }
if ((Get-Content (Join-Path $aside[0].FullName "logs\10.0.0.2\$day.log") -Raw) -notmatch "from before") { Die "the old folder's contents weren't kept" }
if (Test-Path "$Data\logs\10.0.0.2") { Die "the new data folder still has the old contents" }
$sd = Get-Acl $Data
if ($sd.Owner -ne "BUILTIN\Administrators" -or (DaclOf $Data) -ne $WantDacl) { Die "new data folder: $($sd.Sddl)" }
for ($i = 0; $i -lt 30 -and (Get-Service Woodshed).Status -ne "Running"; $i++) { Start-Sleep 1 }
if ((Get-Service Woodshed).Status -ne "Running") { Die "the service isn't running" }
Ok "user-owned folder moved aside to $($aside[0].Name) with its contents; a fresh protected one replaced it"
Msiexec @("/x", "`"$Msi`"", "DELETE_LOGS=1") (Join-Path $Logs "hardening-uninstall-untrusted.log")
Remove-Item -Recurse -Force $aside[0].FullName
if (Test-Path $Data) { Die "$Data is still there after DELETE_LOGS=1" }

Write-Host "==> W2: with no data folder, the install creates it already protected"
if (Test-Path $Data) { Die "$Data exists before the fresh-install check" }
Msiexec @("/i", "`"$Msi`"") (Join-Path $Logs "hardening-install-fresh.log")
$sd = Get-Acl $Data
if ($sd.Owner -ne "BUILTIN\Administrators" -or (DaclOf $Data) -ne $WantDacl) { Die "fresh data folder: $($sd.Sddl)" }
if (@(Get-ChildItem C:\ProgramData -Directory -Filter "Woodshed.untrusted-*").Count -ne 0) { Die "a fresh install set something aside" }
Ok "fresh install: the data folder is owned by Administrators with the protected DACL, nothing set aside"
Msiexec @("/x", "`"$Msi`"", "DELETE_LOGS=1") (Join-Path $Logs "hardening-uninstall-fresh.log")

Remove-Item -Recurse -Force $Outside -ErrorAction SilentlyContinue
Set-TimeZone -Id $origTz
Write-Host "`n$passes checks passed"
