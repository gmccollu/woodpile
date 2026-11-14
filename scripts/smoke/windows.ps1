<#
.SYNOPSIS
  Install smoke test for Windows (SPEC §7), run by .github/workflows/install-smoke.yml on a
  disposable CI machine. It installs Woodshed for real as an administrator: never run it on a
  machine you care about.

.DESCRIPTION
  -Mode msi     installs -Msi (v1), checks it, upgrades to -UpgradeMsi (v2), uninstalls keeping
                the data, reinstalls and uninstalls with DELETE_LOGS=1.
  -Mode manual  `woodshed install` / `uninstall` with -Binary (no package), the same checks.
  -Mode legacy  the rename from Woodpile: installs -OldMsi (the last Woodpile release candidate),
                checks that -Binary's `woodshed install` refuses beside it, installs -Msi over it
                (a major upgrade; build it with a version at least the old one's), and checks
                that the old product, service and firewall rules are gone, C:\ProgramData\Woodpile
                is kept and named by `woodshed status`, and Woodshed runs.

  Checks: the service runs as NT SERVICE\Woodshed, C:\ProgramData\Woodshed has the MSI's
  protected ACL, the firewall rules exist, UDP and TCP 514 are held by the service, the UI
  answers on 8514, 10,000 messages are all on disk exactly once (scripts/verifyseq), the
  Application event log has Woodshed entries, and the data survives restarts, upgrades and
  uninstalls until it's asked to be deleted.

  Syslog over TLS (SPEC §10.3.6, the 1.1 acceptance items): the MSI and `woodshed install` add
  the rule "Woodshed syslog (TLS 6514)" present but disabled; an MSI upgrade keeps its state
  (enabled stays enabled); the service, as NT SERVICE\Woodshed, reads the rule's state (GET
  /api/tls firewall.state, and the tls_firewall banner while the rule is off); `woodshed tls on`
  enables it and a TLS message from another process arrives (`woodshed send --proto tls --ca`
  with the downloaded CA); `woodshed tls off` disables it; uninstall removes it.
#>
[CmdletBinding()]
param(
    [ValidateSet("msi", "manual", "legacy")][string]$Mode,
    [string]$Msi = "",
    [string]$OldMsi = "",
    [string]$UpgradeMsi = "",
    [string]$Binary = "",
    [string]$VerifySeq = "dist\verifyseq.exe",
    [string]$Logs = "$env:RUNNER_TEMP\smoke-logs"
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Force -Path $Logs | Out-Null

$Data = Join-Path $env:ProgramData "Woodshed"
$Exe = Join-Path $env:ProgramFiles "Woodshed\woodshed.exe"
$Ui = "http://127.0.0.1:8514"
$ServiceSid = "S-1-5-80-246480601-2135328064-241538422-929878615-662582935"
$script:passes = 0

function Say($m) { Write-Host "`n==> $m" }
function Ok($m) { $script:passes++; Write-Host "PASS $m" }
function Collect-Logs {
    try {
        Get-WinEvent -FilterHashtable @{ LogName = "Application"; ProviderName = "Woodshed" } -MaxEvents 200 -ErrorAction SilentlyContinue |
            Format-List TimeCreated, LevelDisplayName, Message | Out-File (Join-Path $Logs "eventlog-$Mode.txt")
        Get-ChildItem -Recurse $Data -ErrorAction SilentlyContinue | Out-File (Join-Path $Logs "data-$Mode.txt")
        sc.exe qc Woodshed | Out-File (Join-Path $Logs "sc-qc-$Mode.txt")
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
            $on514 = @($st.listeners | Where-Object { $_.ok -and $_.port -eq 514 -and $_.proto -in "udp", "tcp" }).Count
            $down = @($st.listeners | Where-Object { -not $_.ok }).Count
            if ($on514 -eq 2 -and $down -eq 0) { return $st }
        } catch { }
        Start-Sleep 1
    }
    Die "the service didn't come up with both listeners on 514 within 60 s"
}

function Check-Running {
    Say "service runs as NT SERVICE\Woodshed, data ACL, firewall, UDP/TCP 514, UI"
    $st = Wait-Up
    $svc = Get-CimInstance Win32_Service -Filter "Name='Woodshed'"
    if (-not $svc) { Die "no Woodshed service" }
    if ($svc.StartName -ne "NT SERVICE\Woodshed") { Die "the service runs as '$($svc.StartName)'" }
    if ($svc.State -ne "Running" -or $svc.StartMode -ne "Auto") { Die "service state $($svc.State), start mode $($svc.StartMode)" }
    Ok "service Woodshed runs as $($svc.StartName) (pid $($svc.ProcessId), auto start)"
    $sidType = (sc.exe qsidtype Woodshed) -join " "
    Write-Host $sidType

    $sddl = (Get-Acl $Data).Sddl
    Write-Host "ACL: $sddl"
    if ($sddl -notmatch "D:P(AI)?\(") { Die "the data ACL isn't protected (inherits from ProgramData)" }
    if ($sddl -notmatch "\(A;OICI;0x1301bf;;;$ServiceSid\)") { Die "the data ACL doesn't give the service account modify" }
    if ($sddl -match ";;;BU\)" -or $sddl -match ";;;AU\)" -or $sddl -match ";;;WD\)") { Die "the data ACL lets ordinary users in" }
    Ok "C:\ProgramData\Woodshed: protected ACL, modify for the service SID, nothing for Users"

    foreach ($p in "UDP", "TCP") {
        $r = Get-NetFirewallRule -DisplayName "Woodshed syslog ($p 514)" -ErrorAction SilentlyContinue
        if (-not $r -or $r.Enabled -ne "True" -or $r.Direction -ne "Inbound" -or $r.Action -ne "Allow") { Die "firewall rule for $p 514 missing or wrong" }
        $port = $r | Get-NetFirewallPortFilter
        if ($port.LocalPort -ne "514") { Die "firewall rule for $p opens $($port.LocalPort)" }
    }
    Ok "firewall rules allow UDP and TCP 514 inbound"
    # (PowerShell names are case-insensitive: don't reuse $Ui here.)
    $uiRule = Get-NetFirewallRule | Where-Object { $_.DisplayName -like "*Woodshed*8514*" }
    if ($uiRule) { Die "a firewall rule opens the UI port" }

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

$TlsRule = "Woodshed syslog (TLS 6514)"

function Check-TlsRule([bool]$enabled, [string]$port = "6514") {
    $r = Get-NetFirewallRule -DisplayName $TlsRule -ErrorAction SilentlyContinue
    if (-not $r) { Die "no firewall rule '$TlsRule'" }
    if (@($r).Count -ne 1) { Die "$(@($r).Count) rules named '$TlsRule'" }
    if ($r.Direction -ne "Inbound" -or $r.Action -ne "Allow") { Die "'$TlsRule' is $($r.Direction)/$($r.Action)" }
    $pf = $r | Get-NetFirewallPortFilter
    if ($pf.Protocol -ne "TCP" -or $pf.LocalPort -ne $port) { Die "'$TlsRule' is $($pf.Protocol) $($pf.LocalPort), not TCP $port" }
    $app = $r | Get-NetFirewallApplicationFilter
    if ($app.Program -notlike "*woodshed.exe") { Die "'$TlsRule' is for program $($app.Program)" }
    $want = if ($enabled) { "True" } else { "False" }
    if ("$($r.Enabled)" -ne $want) { Die "'$TlsRule' Enabled=$($r.Enabled), want $want" }
    Ok "firewall rule '$TlsRule': inbound TCP $port for woodshed.exe, enabled=$want"
}

function Wait-Tls([bool]$on) {
    for ($i = 0; $i -lt 60; $i++) {
        try {
            $st = Invoke-RestMethod -TimeoutSec 2 "$Ui/api/status"
            $t = @($st.listeners | Where-Object { $_.proto -eq "tls" })
            if ($on -and $t.Count -eq 1 -and $t[0].ok) { return $st }
            if (-not $on -and $t.Count -eq 0) { return $st }
        } catch { }
        Start-Sleep 1
    }
    Die "the TLS listener didn't turn $(if ($on) { 'on' } else { 'off' }) within 60 s"
}

# Check-Tls: tls on (the rule is enabled), the service reads the rule's state, 1,000 messages
# over TLS from another process, the banner while the rule is off, tls off.
function Check-Tls([string]$exe) {
    Say "syslog over TLS: woodshed tls on, the rule, the service reads its state, a TLS sender"
    foreach ($f in "tls-ca.pem", "tls-ca-key.pem", "tls-cert.pem", "tls-key.pem") {
        if (Test-Path (Join-Path $Data $f)) { Die "$f exists before TLS was turned on" }
    }
    Ok "no TLS key files before TLS is turned on"
    $out = & $exe tls on 2>&1 | Out-String
    Write-Host $out
    if ($LASTEXITCODE -ne 0) { Die "woodshed tls on exited $LASTEXITCODE" }
    if ($out -notmatch "Enabled the Windows firewall rule") { Die "tls on didn't enable the rule" }
    Wait-Tls $true | Out-Null
    Check-TlsRule $true
    $svc = Get-CimInstance Win32_Service -Filter "Name='Woodshed'"
    $tcp = Get-NetTCPConnection -LocalPort 6514 -State Listen -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $svc.ProcessId
    if (-not $tcp) { Die "the service doesn't listen on TCP 6514" }
    $t = Invoke-RestMethod "$Ui/api/tls"
    if ($t.firewall.state -ne "ok") { Die "GET /api/tls firewall.state = $($t.firewall.state) with the rule on (the service can't read it?)" }
    # PowerShell 7 turns ISO dates into DateTime values: compare in UTC.
    if (-not $t.ca -or ([datetime]$t.ca.not_after).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss") -ne "2037-12-31T23:59:59") {
        Die "the CA: $($t.ca | ConvertTo-Json -Compress)"
    }
    Ok "the service (NT SERVICE\Woodshed) reads the rule as ok; CA $($t.ca.sha256.Substring(0, 23))..., ends $($t.ca.not_after)"

    $ca = Join-Path $env:RUNNER_TEMP "woodshed-ca.pem"
    Invoke-WebRequest -UseBasicParsing "$Ui/api/tls/ca.pem" -OutFile $ca
    $run = New-RunId
    Native $exe @("send", "--host", "127.0.0.1", "--proto", "tls", "--ca", $ca, "--count", "1000", "--rate", "2000", "--run", $run)
    Native $VerifySeq @("-data", $Data, "-run", $run, "-n", "1000", "-wait", "30s")
    Ok "1,000 of 1,000 sent over TLS (checking the downloaded CA) on disk, no duplicates"

    Say "the rule turned off by hand: the service sees it and shows tls_firewall"
    Set-NetFirewallRule -DisplayName $TlsRule -Enabled False
    Restart-Service Woodshed # the state is cached for a minute
    Wait-Tls $true | Out-Null
    $t = Invoke-RestMethod "$Ui/api/tls"
    if ($t.firewall.state -ne "disabled" -or $t.firewall.command -ne "woodshed tls on") { Die "firewall with the rule off: $($t.firewall | ConvertTo-Json -Compress)" }
    $st = Invoke-RestMethod "$Ui/api/status"
    $b = @($st.banners | Where-Object { $_.kind -eq "tls_firewall" })
    if ($b.Count -ne 1 -or $b[0].text -notmatch "woodshed tls on") { Die "no tls_firewall banner: $($st.banners | ConvertTo-Json -Compress)" }
    Ok "the service read the disabled rule (state disabled) and shows tls_firewall: $($b[0].text)"
    Native $exe @("tls", "on")
    Check-TlsRule $true

    $out = & $exe status 2>&1 | Out-String
    if ($out -notmatch "Syslog TLS: +on, TCP 6514" -or $out -notmatch "TLS CA: +SHA-256 ") { Write-Host $out; Die "woodshed status doesn't show TLS" }
    Ok "woodshed status shows the TLS listener and its fingerprints"
}

function Tls-Off([string]$exe) {
    Native $exe @("tls", "off")
    Wait-Tls $false | Out-Null
    Check-TlsRule $false
    if (-not (Test-Path (Join-Path $Data "tls-ca.pem"))) { Die "tls off deleted the CA" }
    Ok "woodshed tls off: the listener is off, the rule disabled, the certificate kept"
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
    $ev = @(Get-WinEvent -FilterHashtable @{ LogName = "Application"; ProviderName = "Woodshed" } -MaxEvents 50 -ErrorAction SilentlyContinue)
    if ($ev.Count -eq 0) { Die "no Woodshed entries in the Application event log" }
    Write-Host ($ev | Select-Object -First 3 | ForEach-Object { "  $($_.TimeCreated) $($_.LevelDisplayName): $($_.Message)" })
    Ok "$($ev.Count) Woodshed entries in the Application event log"
}

function Check-Status([string]$exe) {
    $out = & $exe status 2>&1 | Out-String
    Write-Host $out
    if ($LASTEXITCODE -ne 0) { Die "woodshed status exited $LASTEXITCODE" }
    if ($out -notmatch "Service: +running" -or $out -notmatch "UDP 0\.0\.0\.0:514: listening") { Die "woodshed status output" }
    Ok "woodshed status shows the service and listeners"
}

function Restart-Woodshed {
    Say "restart"
    $before = (Get-CimInstance Win32_Service -Filter "Name='Woodshed'").ProcessId
    Restart-Service Woodshed
    Wait-Up | Out-Null
    $after = (Get-CimInstance Win32_Service -Filter "Name='Woodshed'").ProcessId
    if ($after -eq $before) { Die "the pid didn't change" }
    Ok "restarted (pid $before -> $after)"
}

function Check-Gone {
    Start-Sleep 2
    if (Get-Service Woodshed -ErrorAction SilentlyContinue) { Die "the service still exists" }
    try { Invoke-RestMethod -TimeoutSec 2 "$Ui/healthz" | Out-Null; Die "the UI still answers" } catch { }
    foreach ($p in "UDP", "TCP") {
        if (Get-NetFirewallRule -DisplayName "Woodshed syslog ($p 514)" -ErrorAction SilentlyContinue) { Die "firewall rule $p left behind" }
    }
    if (Get-NetFirewallRule -DisplayName $TlsRule -ErrorAction SilentlyContinue) { Die "firewall rule '$TlsRule' left behind" }
    Ok "service and firewall rules (UDP, TCP and TLS) removed"
}
function Check-DataKept { if (-not (Test-Path (Join-Path $Data "logs"))) { Die "$Data\logs was deleted" }; Ok "$Data kept" }
function Check-DataGone { if (Test-Path $Data) { Die "$Data still exists" }; Ok "$Data deleted" }

function Refuses([string]$want, [string]$exe, [string[]]$a) {
    $out = & $exe @a 2>&1 | Out-String
    if ($LASTEXITCODE -eq 0) { Die "$exe $($a -join ' ') should have refused: $out" }
    if ($out -notmatch [regex]::Escape($want)) { Die "refused for the wrong reason: $out" }
    Ok "woodshed $($a -join ' ') refuses: $($out.Trim().Split("`n")[0])"
}

switch ($Mode) {
    "msi" {
        Say "msiexec /i $Msi"
        Install-Msi $Msi (Join-Path $Logs "install-v1.log")
        $st = Check-Running
        Check-TlsRule $false
        $v1 = $st.version
        $r1 = New-RunId
        Send-Traffic $Exe $r1
        Check-EventLog
        Check-Status $Exe
        Check-Tls $Exe
        Refuses "package already installed" $Exe @("install")
        Refuses "installed by the package" $Exe @("uninstall")
        Restart-Woodshed
        Still-There $r1 "a restart"

        Say "major upgrade to $UpgradeMsi"
        Install-Msi $UpgradeMsi (Join-Path $Logs "upgrade-v2.log")
        $st = Check-Running
        Wait-Tls $true | Out-Null
        Check-TlsRule $true
        Ok "the upgrade kept the TLS rule enabled (and the listener on)"
        if ($st.version -eq $v1) { Die "still version $v1 after the upgrade" }
        $products = @(Get-CimInstance Win32_Product -Filter "Name='Woodshed'")
        if ($products.Count -ne 1) { Die "$($products.Count) Woodshed products installed after the upgrade" }
        Ok "upgraded $v1 -> $($st.version), one product registered"
        Still-There $r1 "the upgrade"
        Tls-Off $Exe
        Say "a repair (reinstall) keeps the disabled rule disabled"
        Install-Msi $UpgradeMsi (Join-Path $Logs "repair-v2.log") @("REINSTALL=ALL", "REINSTALLMODE=vomus")
        Wait-Up | Out-Null
        Check-TlsRule $false

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
    "legacy" {
        $oldData = Join-Path $env:ProgramData "Woodpile"
        Say "the old Woodpile MSI: msiexec /i $OldMsi"
        Install-Msi $OldMsi (Join-Path $Logs "install-woodpile.log")
        Wait-Up | Out-Null
        $old = Get-CimInstance Win32_Service -Filter "Name='Woodpile'"
        if (-not $old -or $old.State -ne "Running") { Die "the old Woodpile service isn't running" }
        $bin = (Resolve-Path $Binary).Path
        Native $bin @("send", "--port", "514", "--proto", "udp", "--count", "100", "--rate", "1000")
        for ($i = 0; ($i -lt 30) -and -not (Test-Path (Join-Path $oldData "logs")); $i++) { Start-Sleep 1 }
        if (-not (Test-Path (Join-Path $oldData "logs"))) { Die "the old Woodpile service wrote no logs in $oldData" }
        Ok "the old Woodpile service runs as $($old.StartName) and keeps its logs in $oldData"
        Refuses "Settings > Apps > Woodpile > Uninstall" $bin @("install")

        Say "the Woodshed MSI over it: msiexec /i $Msi"
        Install-Msi $Msi (Join-Path $Logs "install-over-woodpile.log")
        Check-Running | Out-Null
        Check-TlsRule $false
        if (Get-Service Woodpile -ErrorAction SilentlyContinue) { Die "the old Woodpile service is still there" }
        if (Get-NetFirewallRule -DisplayName "Woodpile syslog (*)" -ErrorAction SilentlyContinue) { Die "the old firewall rules are still there" }
        if (Test-Path "HKLM:\Software\Woodpile") { Die "the old MSI's registry key is still there" }
        if (Test-Path (Join-Path $env:ProgramFiles "Woodpile\woodpile.exe")) { Die "the old woodpile.exe is still there" }
        $products = @(Get-CimInstance Win32_Product | Where-Object { $_.Name -in "Woodpile", "Woodshed" })
        if ($products.Count -ne 1 -or $products[0].Name -ne "Woodshed") { Die "products after the upgrade: $(($products | ForEach-Object Name) -join ', ')" }
        Ok "a major upgrade: only Woodshed is installed; the Woodpile product, service, firewall rules and key are gone"
        if (-not (Test-Path (Join-Path $oldData "logs"))) { Die "the old data in $oldData was deleted" }
        Ok "the old data in $oldData is kept"
        $out = & $Exe status 2>&1 | Out-String
        Write-Host $out
        if ($out -notmatch [regex]::Escape("old Woodpile data in $oldData")) { Die "woodshed status doesn't name the old data folder" }
        if ($out -match "old Woodpile service") { Die "woodshed status still sees the old service" }
        Ok "woodshed status names the old data folder, and no old service"
        $r1 = New-RunId
        Send-Traffic $Exe $r1
        Uninstall-Msi $Msi (Join-Path $Logs "uninstall-delete.log") @("DELETE_LOGS=1")
        Check-Gone
        Check-DataGone
    }
    "manual" {
        $bin = (Resolve-Path $Binary).Path
        # Review M3: an ordinary user pre-creates a child folder that everyone can write.
        New-Item -ItemType Directory -Force -Path (Join-Path $Data "logs") | Out-Null
        icacls (Join-Path $Data "logs") /grant "*S-1-1-0:(OI)(CI)F" | Out-Null
        # A junction planted inside: install removes it (as the MSI does), never follows it.
        $target = Join-Path $env:RUNNER_TEMP "junction-target"
        New-Item -ItemType Directory -Force -Path $target | Out-Null
        Set-Content (Join-Path $target "keep.txt") "keep"
        cmd /c mklink /J (Join-Path $Data "logs\evil-junction") $target | Out-Null
        Say "woodshed install (no package)"
        $installOut = & $bin install 2>&1 | Out-String
        Write-Host $installOut
        if ($LASTEXITCODE -ne 0) { Die "woodshed install exited $LASTEXITCODE" }
        if (Test-Path (Join-Path $Data "logs\evil-junction")) { Die "the planted junction survived the install" }
        if (-not (Test-Path (Join-Path $target "keep.txt"))) { Die "the junction's target was changed" }
        if ($installOut -notmatch "removed .*evil-junction") { Die "install didn't say it removed the junction" }
        Ok "install removed a planted junction (and said so), leaving its target alone"
        Check-Running | Out-Null
        Check-TlsRule $false
        $child = (Get-Acl (Join-Path $Data "logs")).Sddl
        Write-Host "logs ACL: $child"
        if ($child -match ";;;WD\)") { Die "the planted Everyone entry on logs survived the install" }
        Ok "install reset the ACL of a pre-created child folder (no Everyone entry)"
        $privs = (sc.exe qprivs Woodshed) -join " "
        Write-Host $privs
        if ($privs -notmatch "SeChangeNotifyPrivilege" -or $privs -match "SeImpersonatePrivilege") { Die "required privileges aren't limited" }
        Ok "the service's required privileges are only SeChangeNotifyPrivilege"
        $r1 = New-RunId
        Send-Traffic $Exe $r1
        Check-EventLog
        Check-Status $Exe
        Check-Tls $Exe
        Tls-Off $Exe
        Refuses "already installed" $bin @("install")
        Restart-Woodshed
        Still-There $r1 "a restart"
        Say "woodshed uninstall keeps the data"
        Native $bin @("uninstall")
        Check-Gone
        Check-DataKept
        Still-There $r1 "woodshed uninstall"
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
