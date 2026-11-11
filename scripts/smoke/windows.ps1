<#
.SYNOPSIS
  Install smoke test for Windows (SPEC §7), run by .github/workflows/install-smoke.yml on a
  disposable CI machine. It installs Woodpile for real as an administrator: never run it on a
  machine you care about.

.DESCRIPTION
  -Mode msi     installs -Msi (v1), checks it, upgrades to -UpgradeMsi (v2), uninstalls keeping
                the data, reinstalls and uninstalls with DELETE_LOGS=1.
  -Mode manual  `woodpile install` / `uninstall` with -Binary (no package), the same checks.

  Checks: the service runs as NT SERVICE\Woodpile, C:\ProgramData\Woodpile has the MSI's
  protected ACL, the firewall rules exist, UDP and TCP 514 are held by the service, the UI
  answers on 8514, 10,000 messages are all on disk exactly once (scripts/verifyseq), the
  Application event log has Woodpile entries, and the data survives restarts, upgrades and
  uninstalls until it's asked to be deleted.
#>
[CmdletBinding()]
param(
    [ValidateSet("msi", "manual")][string]$Mode,
    [string]$Msi = "",
    [string]$UpgradeMsi = "",
    [string]$Binary = "",
    [string]$VerifySeq = "dist\verifyseq.exe",
    [string]$Logs = "$env:RUNNER_TEMP\smoke-logs"
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Force -Path $Logs | Out-Null

$Data = Join-Path $env:ProgramData "Woodpile"
$Exe = Join-Path $env:ProgramFiles "Woodpile\woodpile.exe"
$Ui = "http://127.0.0.1:8514"
$ServiceSid = "S-1-5-80-276366627-2944175572-1807786847-528126453-1705305312"
$script:passes = 0

function Say($m) { Write-Host "`n==> $m" }
function Ok($m) { $script:passes++; Write-Host "PASS $m" }
function Collect-Logs {
    try {
        Get-WinEvent -FilterHashtable @{ LogName = "Application"; ProviderName = "Woodpile" } -MaxEvents 200 -ErrorAction SilentlyContinue |
            Format-List TimeCreated, LevelDisplayName, Message | Out-File (Join-Path $Logs "eventlog-$Mode.txt")
        Get-ChildItem -Recurse $Data -ErrorAction SilentlyContinue | Out-File (Join-Path $Logs "data-$Mode.txt")
        sc.exe qc Woodpile | Out-File (Join-Path $Logs "sc-qc-$Mode.txt")
    } catch { }
}
function Die($m) { Write-Host "FAIL $m"; Collect-Logs; exit 1 }
function Native([string]$exe, [string[]]$a) {
    & $exe @a
    if ($LASTEXITCODE -ne 0) { Die "$exe $($a -join ' ') exited $LASTEXITCODE" }
}

function Install-Msi([string]$path, [string]$log, [string[]]$extra = @()) {
    $p = Start-Process msiexec.exe -Wait -PassThru -ArgumentList (@("/i", "`"$path`"", "/qn", "/l*v", "`"$log`"") + $extra)
    if ($p.ExitCode -ne 0) { Copy-Item $log $Logs -ErrorAction SilentlyContinue; Die "msiexec /i $path exited $($p.ExitCode)" }
}
function Uninstall-Msi([string]$path, [string]$log, [string[]]$extra = @()) {
    $p = Start-Process msiexec.exe -Wait -PassThru -ArgumentList (@("/x", "`"$path`"", "/qn", "/l*v", "`"$log`"") + $extra)
    if ($p.ExitCode -ne 0) { Copy-Item $log $Logs -ErrorAction SilentlyContinue; Die "msiexec /x $path exited $($p.ExitCode)" }
}

function Wait-Up {
    for ($i = 0; $i -lt 60; $i++) {
        try {
            $st = Invoke-RestMethod -TimeoutSec 2 "$Ui/api/status"
            if ($st.listeners.Count -eq 2 -and @($st.listeners | Where-Object { $_.ok -and $_.port -eq 514 }).Count -eq 2) { return $st }
        } catch { }
        Start-Sleep 1
    }
    Die "the service didn't come up with both listeners on 514 within 60 s"
}

function Check-Running {
    Say "service runs as NT SERVICE\Woodpile, data ACL, firewall, UDP/TCP 514, UI"
    $st = Wait-Up
    $svc = Get-CimInstance Win32_Service -Filter "Name='Woodpile'"
    if (-not $svc) { Die "no Woodpile service" }
    if ($svc.StartName -ne "NT SERVICE\Woodpile") { Die "the service runs as '$($svc.StartName)'" }
    if ($svc.State -ne "Running" -or $svc.StartMode -ne "Auto") { Die "service state $($svc.State), start mode $($svc.StartMode)" }
    Ok "service Woodpile runs as $($svc.StartName) (pid $($svc.ProcessId), auto start)"
    $sidType = (sc.exe qsidtype Woodpile) -join " "
    Write-Host $sidType

    $sddl = (Get-Acl $Data).Sddl
    Write-Host "ACL: $sddl"
    if ($sddl -notmatch "D:PAI") { Die "the data ACL isn't protected (inherits from ProgramData)" }
    if ($sddl -notmatch "\(A;OICI;0x1301bf;;;$ServiceSid\)") { Die "the data ACL doesn't give the service account modify" }
    if ($sddl -match ";;;BU\)" -or $sddl -match ";;;AU\)" -or $sddl -match ";;;WD\)") { Die "the data ACL lets ordinary users in" }
    Ok "C:\ProgramData\Woodpile: protected ACL, modify for the service SID, nothing for Users"

    foreach ($p in "UDP", "TCP") {
        $r = Get-NetFirewallRule -DisplayName "Woodpile syslog ($p 514)" -ErrorAction SilentlyContinue
        if (-not $r -or $r.Enabled -ne "True" -or $r.Direction -ne "Inbound" -or $r.Action -ne "Allow") { Die "firewall rule for $p 514 missing or wrong" }
        $port = $r | Get-NetFirewallPortFilter
        if ($port.LocalPort -ne "514") { Die "firewall rule for $p opens $($port.LocalPort)" }
    }
    Ok "firewall rules allow UDP and TCP 514 inbound"
    $ui = Get-NetFirewallRule | Where-Object { $_.DisplayName -like "*Woodpile*8514*" }
    if ($ui) { Die "a firewall rule opens the UI port" }

    $udp = Get-NetUDPEndpoint -LocalPort 514 -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $svc.ProcessId
    $tcp = Get-NetTCPConnection -LocalPort 514 -State Listen -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $svc.ProcessId
    if (-not $udp) { Die "the service doesn't hold UDP 514" }
    if (-not $tcp) { Die "the service doesn't listen on TCP 514" }
    Ok "pid $($svc.ProcessId) holds UDP 514 and listens on TCP 514"

    $h = Invoke-RestMethod "$Ui/healthz"
    if (-not $h.ok) { Die "/healthz" }
    $r = Invoke-WebRequest -UseBasicParsing "$Ui/"
    if ($r.StatusCode -ne 200) { Die "the UI answered $($r.StatusCode)" }
    Ok "UI answers on $Ui (version $($st.version))"
    return $st
}

function New-RunId { -join ((1..8) | ForEach-Object { "{0:x}" -f (Get-Random -Maximum 16) }) }

function Send-Traffic([string]$exe, [string]$run) {
    Say "send 10,000 messages (run $run) to 127.0.0.1:514 and find them on disk"
    Native $exe @("send", "--port", "514", "--proto", "udp", "--count", "5000", "--rate", "2000", "--run", $run, "--first-seq", "0")
    Native $exe @("send", "--port", "514", "--proto", "tcp", "--count", "5000", "--rate", "5000", "--run", $run, "--first-seq", "5000")
    Native $VerifySeq @("-data", $Data, "-run", $run, "-n", "10000", "-wait", "30s")
    Ok "10,000 of 10,000 on disk, no duplicates"
}
function Still-There([string]$run, [string]$after) {
    Native $VerifySeq @("-data", $Data, "-run", $run, "-n", "10000")
    Ok "run $run still complete after $after"
}

function Check-EventLog {
    $ev = @(Get-WinEvent -FilterHashtable @{ LogName = "Application"; ProviderName = "Woodpile" } -MaxEvents 50 -ErrorAction SilentlyContinue)
    if ($ev.Count -eq 0) { Die "no Woodpile entries in the Application event log" }
    Write-Host ($ev | Select-Object -First 3 | ForEach-Object { "  $($_.TimeCreated) $($_.LevelDisplayName): $($_.Message)" })
    Ok "$($ev.Count) Woodpile entries in the Application event log"
}

function Check-Status([string]$exe) {
    $out = & $exe status 2>&1 | Out-String
    Write-Host $out
    if ($LASTEXITCODE -ne 0) { Die "woodpile status exited $LASTEXITCODE" }
    if ($out -notmatch "Service: +running" -or $out -notmatch "UDP 0\.0\.0\.0:514: listening") { Die "woodpile status output" }
    Ok "woodpile status shows the service and listeners"
}

function Restart-Woodpile {
    Say "restart"
    $before = (Get-CimInstance Win32_Service -Filter "Name='Woodpile'").ProcessId
    Restart-Service Woodpile
    Wait-Up | Out-Null
    $after = (Get-CimInstance Win32_Service -Filter "Name='Woodpile'").ProcessId
    if ($after -eq $before) { Die "the pid didn't change" }
    Ok "restarted (pid $before -> $after)"
}

function Check-Gone {
    Start-Sleep 2
    if (Get-Service Woodpile -ErrorAction SilentlyContinue) { Die "the service still exists" }
    try { Invoke-RestMethod -TimeoutSec 2 "$Ui/healthz" | Out-Null; Die "the UI still answers" } catch { }
    foreach ($p in "UDP", "TCP") {
        if (Get-NetFirewallRule -DisplayName "Woodpile syslog ($p 514)" -ErrorAction SilentlyContinue) { Die "firewall rule $p left behind" }
    }
    Ok "service and firewall rules removed"
}
function Check-DataKept { if (-not (Test-Path (Join-Path $Data "logs"))) { Die "$Data\logs was deleted" }; Ok "$Data kept" }
function Check-DataGone { if (Test-Path $Data) { Die "$Data still exists" }; Ok "$Data deleted" }

function Refuses([string]$want, [string]$exe, [string[]]$a) {
    $out = & $exe @a 2>&1 | Out-String
    if ($LASTEXITCODE -eq 0) { Die "$exe $($a -join ' ') should have refused: $out" }
    if ($out -notmatch [regex]::Escape($want)) { Die "refused for the wrong reason: $out" }
    Ok "woodpile $($a -join ' ') refuses: $($out.Trim().Split("`n")[0])"
}

switch ($Mode) {
    "msi" {
        Say "msiexec /i $Msi"
        Install-Msi $Msi (Join-Path $Logs "install-v1.log")
        $st = Check-Running
        $v1 = $st.version
        $r1 = New-RunId
        Send-Traffic $Exe $r1
        Check-EventLog
        Check-Status $Exe
        Refuses "package already installed" $Exe @("install")
        Refuses "installed by the package" $Exe @("uninstall")
        Restart-Woodpile
        Still-There $r1 "a restart"

        Say "major upgrade to $UpgradeMsi"
        Install-Msi $UpgradeMsi (Join-Path $Logs "upgrade-v2.log")
        $st = Check-Running
        if ($st.version -eq $v1) { Die "still version $v1 after the upgrade" }
        $products = @(Get-CimInstance Win32_Product -Filter "Name='Woodpile'")
        if ($products.Count -ne 1) { Die "$($products.Count) Woodpile products installed after the upgrade" }
        Ok "upgraded $v1 -> $($st.version), one product registered"
        Still-There $r1 "the upgrade"

        Say "uninstall keeps the data"
        Uninstall-Msi $UpgradeMsi (Join-Path $Logs "uninstall.log")
        Check-Gone
        Check-DataKept
        if (Test-Path $Exe) { Die "$Exe is still there" }
        Still-There $r1 "the uninstall"

        Say "reinstall over the kept data, then uninstall with DELETE_LOGS=1"
        Install-Msi $UpgradeMsi (Join-Path $Logs "reinstall.log")
        Wait-Up | Out-Null
        Still-There $r1 "the reinstall"
        Uninstall-Msi $UpgradeMsi (Join-Path $Logs "uninstall-delete.log") @("DELETE_LOGS=1")
        Check-Gone
        Check-DataGone
    }
    "manual" {
        $bin = (Resolve-Path $Binary).Path
        Say "woodpile install (no package)"
        Native $bin @("install")
        Check-Running | Out-Null
        $r1 = New-RunId
        Send-Traffic $Exe $r1
        Check-EventLog
        Check-Status $Exe
        Refuses "already installed" $bin @("install")
        Restart-Woodpile
        Still-There $r1 "a restart"
        Say "woodpile uninstall keeps the data"
        Native $bin @("uninstall")
        Check-Gone
        Check-DataKept
        Still-There $r1 "woodpile uninstall"
        Say "install again over the kept data, then uninstall --delete-logs"
        Native $bin @("install")
        Wait-Up | Out-Null
        Still-There $r1 "a reinstall"
        Native $bin @("uninstall", "--delete-logs")
        Check-Gone
        Check-DataGone
    }
}
Collect-Logs
Write-Host "`n$Mode smoke test (Windows): $script:passes checks passed"
