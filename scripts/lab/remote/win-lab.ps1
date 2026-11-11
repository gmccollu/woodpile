<#
.SYNOPSIS
  Woodpile lab, Windows VM side. Copied to C:\woodpile-lab by `wl stage`; called by the driver over
  OpenSSH as an administrator:  powershell -NoProfile -ExecutionPolicy Bypass -File C:\woodpile-lab\win-lab.ps1 <Action> [args]

.DESCRIPTION
  install MSI                     msiexec /i /qn with a verbose log
  svc                             JSON: service state, start type, account, sockets, UI, woodpile status
  fw-state                        JSON: profiles enabled, the MSI's rules and their state
  fw-enable                       turn every firewall profile on (the OpenSSH rule must exist first)
  fw-open | fw-close              enable / disable the MSI's Woodpile rules (the negative control)
  verify RUN N [MISSING_OK] [WAIT]   verifyseq -json against C:\ProgramData\Woodpile
  stats                           GET /api/stats
  devices                         device folders under logs\
  sample OUT INTERVAL SECS [PID]  CSV of working set, private bytes, CPU seconds, handles, threads
  soak-sampler start SECS|stop|status   the sampler as a SYSTEM scheduled task (survives logoff)
  verify-detached RUN N OUTJSON   verifyseq as a scheduled task (long runs)
  portable start|stop|status PORT [allow]   the zip's woodpile.exe by hand on PORT; "allow" adds a temporary rule
  reboot                          Restart-Computer in 5 s
  collect OUT.zip                 event log, service config, firewall rules, config, status, data listing, samples
#>
param([Parameter(Position = 0, Mandatory = $true)][string]$Action,
      [Parameter(Position = 1, ValueFromRemainingArguments = $true)][string[]]$Rest)
$ErrorActionPreference = "Stop"
$L = "C:\woodpile-lab"
$Data = Join-Path $env:ProgramData "Woodpile"
$Ui = "http://127.0.0.1:8514"
function Arg($i, $default = $null) { if ($Rest -and $Rest.Count -gt $i) { $Rest[$i] } else { $default } }
function SvcPid { (Get-CimInstance Win32_Service -Filter "Name='Woodpile'").ProcessId }
function MsiRules { Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like "*Woodpile*" } }

switch ($Action) {
  "install" {
    $msi = (Resolve-Path (Arg 0)).Path
    $log = Join-Path $L "msi-install.log"
    $p = Start-Process msiexec.exe -Wait -PassThru -ArgumentList @("/i", "`"$msi`"", "/qn", "/l*v", "`"$log`"")
    "msiexec exit $($p.ExitCode)"
    exit $p.ExitCode
  }
  "svc" {
    $s = Get-CimInstance Win32_Service -Filter "Name='Woodpile'"
    $procId = if ($s) { $s.ProcessId } else { 0 }
    $udp = @(Get-NetUDPEndpoint -LocalPort 514 -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $procId).Count
    $tcp = @(Get-NetTCPConnection -LocalPort 514 -State Listen -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $procId).Count
    $health = try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 "$Ui/healthz").Content } catch { "" }
    $status = try { (& "$env:ProgramFiles\Woodpile\woodpile.exe" status 2>&1 | Out-String) } catch { "$_" }
    [ordered]@{ state = $s.State; start_mode = $s.StartMode; account = $s.StartName; pid = $procId
      udp514_sockets = $udp; tcp514_listen = $tcp; healthz = $health; woodpile_status = $status } | ConvertTo-Json -Compress
  }
  "fw-state" {
    $prof = Get-NetFirewallProfile | ForEach-Object { "$($_.Name)=$($_.Enabled)" }
    $active = (Get-NetConnectionProfile | ForEach-Object { "$($_.InterfaceAlias):$($_.NetworkCategory)" })
    $rules = MsiRules | ForEach-Object {
      $pf = $_ | Get-NetFirewallPortFilter; $af = $_ | Get-NetFirewallApplicationFilter
      "$($_.DisplayName) [$($_.Direction) $($_.Action) enabled=$($_.Enabled) profile=$($_.Profile) $($pf.Protocol)/$($pf.LocalPort) $($af.Program)]" }
    [ordered]@{ profiles = $prof; nic_profiles = $active; woodpile_rules = @($rules) } | ConvertTo-Json -Compress
  }
  "fw-enable" {
    if (-not (Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.Enabled -eq "True" -and ($_ | Get-NetFirewallPortFilter).LocalPort -eq 22 })) {
      throw "no enabled firewall rule for SSH (22): refusing to enable the firewall"
    }
    Set-NetFirewallProfile -Profile Domain, Private, Public -Enabled True
    Get-NetFirewallProfile | ForEach-Object { "$($_.Name)=$($_.Enabled)" }
  }
  "fw-open" { MsiRules | Enable-NetFirewallRule; "enabled $(@(MsiRules).Count) Woodpile rules" }
  "fw-close" { MsiRules | Disable-NetFirewallRule; "disabled $(@(MsiRules).Count) Woodpile rules (negative control)" }
  "verify" {
    $dataDir = if ($env:WL_DATA) { $env:WL_DATA } else { $Data }
    & "$L\verifyseq.exe" -data $dataDir -run (Arg 0) -n (Arg 1) -missing-ok (Arg 2 0) -wait (Arg 3 "30s") -json
    exit 0
  }
  "verify-detached" {
    $run = Arg 0; $n = Arg 1; $out = Arg 2
    $cmd = "& '$L\verifyseq.exe' -data '$Data' -run $run -n $n -json > '$out.tmp' 2>&1; Move-Item -Force '$out.tmp' '$out'"
    $a = New-ScheduledTaskAction -Execute "powershell.exe" -Argument "-NoProfile -Command `"$cmd`""
    Register-ScheduledTask -TaskName "woodpile-lab-verify-$run" -Action $a -User SYSTEM -RunLevel Highest -Force | Out-Null
    Start-ScheduledTask -TaskName "woodpile-lab-verify-$run"
    "started woodpile-lab-verify-$run -> $out"
  }
  "stats" { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "$Ui/api/stats").Content }
  "devices" { $dataDir = if ($env:WL_DATA) { $env:WL_DATA } else { $Data }; Get-ChildItem (Join-Path $dataDir "logs") -Directory | ForEach-Object Name }
  "sample" {
    $out = Arg 0; $iv = [int](Arg 1 1); $secs = [int](Arg 2 60); $procId = Arg 3
    "time,elapsed_s,pid,ws_kb,private_kb,cpu_s,handles,threads" | Out-File -Encoding ascii $out
    $t0 = Get-Date
    while (((Get-Date) - $t0).TotalSeconds -lt $secs) {
      $id = if ($procId) { [int]$procId } else { SvcPid }
      $p = Get-Process -Id $id -ErrorAction SilentlyContinue
      if ($p) {
        $line = "{0},{1},{2},{3},{4},{5},{6},{7}" -f (Get-Date).ToUniversalTime().ToString("HH:mm:ss"), [int]((Get-Date) - $t0).TotalSeconds,
          $id, [int]($p.WorkingSet64 / 1KB), [int]($p.PrivateMemorySize64 / 1KB), [math]::Round($p.TotalProcessorTime.TotalSeconds, 2), $p.HandleCount, $p.Threads.Count
        Add-Content -Encoding ascii $out $line
      }
      Start-Sleep -Seconds $iv
    }
  }
  "soak-sampler" {
    switch (Arg 0) {
      "start" {
        $secs = Arg 1
        $a = New-ScheduledTaskAction -Execute "powershell.exe" -Argument "-NoProfile -ExecutionPolicy Bypass -File $L\win-lab.ps1 sample $L\soak-samples.csv 60 $secs"
        $set = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Seconds ([int]$secs + 3600))
        Register-ScheduledTask -TaskName "woodpile-lab-soak-sampler" -Action $a -Settings $set -User SYSTEM -RunLevel Highest -Force | Out-Null
        Start-ScheduledTask -TaskName "woodpile-lab-soak-sampler"
        "sampler started for $secs s -> $L\soak-samples.csv"
      }
      "stop" { Stop-ScheduledTask -TaskName "woodpile-lab-soak-sampler" -ErrorAction SilentlyContinue }
      "status" { (Get-ScheduledTask -TaskName "woodpile-lab-soak-sampler").State; Get-Content "$L\soak-samples.csv" -Tail 3 -ErrorAction SilentlyContinue }
    }
  }
  "portable" {
    $op = Arg 0; $port = Arg 1 15514; $allow = (Arg 2 "") -eq "allow"
    $P = Join-Path $L "portable"
    switch ($op) {
      "start" {
        New-Item -ItemType Directory -Force (Join-Path $P "data") | Out-Null
        $dataDir = (Join-Path $P "data") -replace '\\', '/'
        @"
[listeners.udp]
enabled = true
port = $port

[listeners.tcp]
enabled = true
port = $port

[storage]
data_dir = "$dataDir"

[ui]
port = 18514
"@ | Out-File -Encoding ascii (Join-Path $P "woodpile.toml")
        # A scheduled task as the lab admin, not a child of this ssh session (OpenSSH kills those on exit).
        $a = New-ScheduledTaskAction -Execute (Join-Path $P "woodpile.exe") -Argument "run --config `"$P\woodpile.toml`"" -WorkingDirectory $P
        Register-ScheduledTask -TaskName "woodpile-lab-portable" -Action $a -User SYSTEM -Force | Out-Null
        Start-ScheduledTask -TaskName "woodpile-lab-portable"
        if ($allow) {
          New-NetFirewallRule -DisplayName "woodpile-lab portable $port UDP" -Direction Inbound -Action Allow -Protocol UDP -LocalPort $port | Out-Null
          New-NetFirewallRule -DisplayName "woodpile-lab portable $port TCP" -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port | Out-Null
          "temporary allow rules for $port added"
        }
        foreach ($i in 1..30) { try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 1 "http://127.0.0.1:18514/healthz" | Out-Null; break } catch { Start-Sleep 1 } }
        "portable started on UDP/TCP $port, data $P\data"
      }
      "stop" {
        Stop-ScheduledTask -TaskName "woodpile-lab-portable" -ErrorAction SilentlyContinue
        Get-Process woodpile -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$P*" } | Stop-Process -Force
        Unregister-ScheduledTask -TaskName "woodpile-lab-portable" -Confirm:$false -ErrorAction SilentlyContinue
        Get-NetFirewallRule -DisplayName "woodpile-lab portable *" -ErrorAction SilentlyContinue | Remove-NetFirewallRule
        "portable stopped, temporary rules removed"
      }
      "status" { Get-Process woodpile -ErrorAction SilentlyContinue | Select-Object Id, Path | Format-Table -AutoSize | Out-String }
    }
  }
  "reboot" { Start-Process -WindowStyle Hidden powershell.exe -ArgumentList "-NoProfile -Command Start-Sleep 5; Restart-Computer -Force"; "rebooting in 5 s" }
  "collect" {
    $out = Arg 0
    $d = Join-Path $env:TEMP ("wl-collect-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
    New-Item -ItemType Directory -Force $d | Out-Null
    Get-WinEvent -FilterHashtable @{ LogName = "Application"; ProviderName = "Woodpile" } -MaxEvents 2000 -ErrorAction SilentlyContinue |
      Format-List TimeCreated, LevelDisplayName, Id, Message | Out-File (Join-Path $d "eventlog.txt")
    sc.exe qc Woodpile | Out-File (Join-Path $d "sc-qc.txt")
    sc.exe qfailure Woodpile | Out-File (Join-Path $d "sc-qfailure.txt")
    MsiRules | Format-List * | Out-File (Join-Path $d "firewall-rules.txt")
    Get-NetFirewallProfile | Format-List Name, Enabled, DefaultInboundAction | Out-File (Join-Path $d "firewall-profiles.txt")
    Copy-Item (Join-Path $Data "woodpile.toml") $d -ErrorAction SilentlyContinue
    try { & "$env:ProgramFiles\Woodpile\woodpile.exe" status *> (Join-Path $d "woodpile-status.txt") } catch { }
    Get-ChildItem -Recurse $Data -ErrorAction SilentlyContinue | Select-Object FullName, Length, LastWriteTimeUtc | Out-File (Join-Path $d "data-listing.txt")
    (Get-Acl $Data).Sddl | Out-File (Join-Path $d "data-acl.txt")
    Copy-Item "$L\*.csv", "$L\*.json", "$L\*.log" $d -ErrorAction SilentlyContinue
    Get-ComputerInfo -Property OsName, OsVersion, OsBuildNumber | Out-File (Join-Path $d "os.txt")
    Compress-Archive -Force -Path "$d\*" -DestinationPath $out
    Remove-Item -Recurse -Force $d
    $out
  }
  default { throw "win-lab.ps1: unknown action $Action" }
}
