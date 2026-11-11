<#
.SYNOPSIS
  Portable-mode smoke test for Windows, run by the install-smoke workflow on a disposable
  CI machine (it binds the real port 514).

.DESCRIPTION
  Starts woodpile.exe with no arguments in a console window of its own (as a double-click
  from Explorer does), checks the banner (data next to the exe, UDP and TCP 514, the UI on
  8514, the firewall note), sends 10,000 messages and finds them all on disk, then closes
  the console window (CTRL_CLOSE_EVENT, scripts/smoke/consoleclose) and checks everything
  was flushed within Windows' ~5 s grace.
#>
[CmdletBinding()]
param(
    [string]$Binary = "dist\woodpile.exe",
    [string]$VerifySeq = "dist\verifyseq.exe",
    [string]$ConsoleClose = "dist\consoleclose.exe",
    [string]$Logs = "$env:RUNNER_TEMP\smoke-logs"
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Force -Path $Logs | Out-Null
$script:passes = 0
function Ok($m) { $script:passes++; Write-Host "PASS $m" }
function Die($m) {
    Write-Host "FAIL $m"
    Copy-Item "$work\*.out" $Logs -ErrorAction SilentlyContinue
    exit 1
}

$work = Join-Path $env:RUNNER_TEMP "portable"
$dl = Join-Path $work "Downloads"
New-Item -ItemType Directory -Force -Path $dl | Out-Null
Copy-Item (Resolve-Path $Binary) (Join-Path $dl "woodpile.exe")
$out = Join-Path $work "console.out"
$data = Join-Path $dl "woodpile-data"

Write-Host "==> woodpile.exe with no arguments, in a new console window"
# Start-Process gives it a console of its own (what Explorer does); stdout also goes to a file.
$p = Start-Process -FilePath (Join-Path $dl "woodpile.exe") -WorkingDirectory $dl -PassThru `
    -RedirectStandardOutput $out -RedirectStandardError (Join-Path $work "console.err.out")
for ($i = 0; $i -lt 60 -and -not ((Test-Path $out) -and (Get-Content -Raw $out) -match "Press Ctrl-C"); $i++) { Start-Sleep 1 }
$banner = Get-Content -Raw $out
Write-Host $banner
if ($banner -notmatch "Press Ctrl-C") { Die "no banner within 60 s" }
if ($banner -match "Starting in portable mode") { Die "the terminal note was printed for a console of its own" }
Ok "a console of its own is treated as a double-click (no terminal note)"
if ($banner -notmatch [regex]::Escape("Data folder:  $data")) { Die "the data folder isn't next to the exe" }
if ($banner -notmatch "Listening on UDP 514 and TCP 514") { Die "not listening on UDP and TCP 514" }
if ($banner -notmatch [regex]::Escape("http://localhost:8514/")) { Die "the UI isn't on 8514" }
if ($banner -notmatch "Choose Allow so devices can") { Die "no firewall note" }
if ($banner -notmatch "netsh advfirewall firewall add rule" -and $banner -notmatch "A firewall rule already lets devices reach") { Die "neither the netsh command nor an existing allow rule" }
Ok "banner: data next to the exe, UDP and TCP 514, UI on 8514, the firewall note and command"
$h = Invoke-RestMethod "http://127.0.0.1:8514/healthz"
if (-not $h.ok) { Die "the UI doesn't answer" }
Ok "the UI answers"

$run = -join ((1..8) | ForEach-Object { "{0:x}" -f (Get-Random -Maximum 16) })
$exe = Join-Path $dl "woodpile.exe"
& $exe send --port 514 --proto udp --count 5000 --rate 2000 --run $run --first-seq 0 | Out-Null
& $exe send --port 514 --proto tcp --count 5000 --rate 5000 --run $run --first-seq 5000 | Out-Null
& (Resolve-Path $VerifySeq) -data $data -run $run -n 10000 -wait 30s
if ($LASTEXITCODE -ne 0) { Die "messages missing on disk" }
Ok "10,000 of 10,000 on disk"
for ($i = 0; $i -lt 20 -and (Get-Content -Raw $out) -notmatch "New device 127\.0\.0\.1 \(first message"; $i++) { Start-Sleep 1 }
if ((Get-Content -Raw $out) -notmatch "New device 127\.0\.0\.1") { Die "no new-device line" }
Ok "a new-device line"

function Close-Console($proc, $logFile, $data, $run, $count) {
    # Closes $proc's console window and checks a clean flush within Windows' ~5 s grace.
    $t0 = Get-Date
    $how = & (Resolve-Path $ConsoleClose) $proc.Id
    $cc = $LASTEXITCODE
    Write-Host "consoleclose: $how (exit $cc)"
    if ($cc -eq 3) { return $false }
    if ($cc -ne 0) { Die "couldn't close the console window" }
    if (-not $proc.WaitForExit(10000)) { Die "still running 10 s after the console window closed ($how)" }
    $took = ((Get-Date) - $t0).TotalSeconds
    if ($took -gt 5) { Die "stopping took $took s; Windows allows about 5 s" }
    $log = Get-Content -Raw $logFile
    Write-Host ("exit code 0x{0:X8}; the log's last lines:" -f $proc.ExitCode)
    Write-Host (($log -split "`n" | Select-Object -Last 8) -join "`n")
    Copy-Item $logFile $Logs -ErrorAction SilentlyContinue
    if ($log -notmatch "stopped cleanly") { Die "woodpile.log has no clean stop" }
    Ok ("closing the console window (CTRL_CLOSE_EVENT) stopped it cleanly in {0:N1} s" -f $took)
    & (Resolve-Path $VerifySeq) -data $data -run $run -n $count | Write-Host
    if ($LASTEXITCODE -ne 0) { Die "messages lost at the close" }
    Ok "$count of $count still on disk after the close"
    return $true
}

Write-Host "==> close the console window (CTRL_CLOSE_EVENT)"
Start-Sleep 1
if (-not (Close-Console $p (Join-Path $data "woodpile.log") $data $run 10000)) {
    # The runner's consoles are pseudo consoles (ConPTY), whose window ignores close
    # requests. Stop this copy with Ctrl-C's equivalent and run a second one in a classic
    # console (conhost.exe), whose X button does send CTRL_CLOSE_EVENT.
    Write-Host "NOTE this session's consoles are pseudo consoles; trying a classic conhost.exe window"
    Stop-Process -Id $p.Id -Force
    $p.WaitForExit(10000) | Out-Null
    $data2 = Join-Path $work "classic"
    $c = Start-Process -FilePath "$env:SystemRoot\System32\conhost.exe" -PassThru `
        -ArgumentList "`"$exe`" portable --no-browser --dir `"$data2`""
    $wp = $null
    for ($i = 0; $i -lt 60 -and -not $wp; $i++) {
        Start-Sleep 1
        $wp = Get-CimInstance Win32_Process -Filter "Name='woodpile.exe'" | Where-Object { $_.ParentProcessId -eq $c.Id } | Select-Object -First 1
    }
    if (-not $wp) { Die "woodpile.exe didn't start under conhost.exe" }
    for ($i = 0; $i -lt 30; $i++) { try { Invoke-RestMethod -TimeoutSec 2 "http://127.0.0.1:8514/healthz" | Out-Null; break } catch { Start-Sleep 1 } }
    $run2 = -join ((1..8) | ForEach-Object { "{0:x}" -f (Get-Random -Maximum 16) })
    & $exe send --port 514 --proto tcp --count 2000 --rate 5000 --run $run2 --first-seq 0 | Out-Null
    & (Resolve-Path $VerifySeq) -data $data2 -run $run2 -n 2000 -wait 30s | Write-Host
    if ($LASTEXITCODE -ne 0) { Die "messages missing under conhost" }
    $wproc = Get-Process -Id $wp.ProcessId
    if (-not (Close-Console $wproc (Join-Path $data2 "woodpile.log") $data2 $run2 2000)) {
        Write-Host "NOTE even conhost.exe gives a pseudo console here; CTRL_CLOSE_EVENT is unproven on this runner"
    }
}
# ---- re-test N2: data-folder ACLs for `woodpile run` ----
function Broad($dir) { $sddl = (Get-Acl $dir).Sddl; Write-Host "  $dir ACL: $sddl"; return ($sddl -match ";;;(BU|AU|WD|IU)\)" -or $sddl -match ";;;S-1-5-32-545\)") }
function Wait-UI($port) {
    for ($i = 0; $i -lt 30; $i++) { try { Invoke-RestMethod -TimeoutSec 2 "http://127.0.0.1:$port/healthz" | Out-Null; return $true } catch { Start-Sleep 1 } }
    return $false
}
function Write-TestConfig($dir) {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $d = $dir -replace '\\', '\\'
    Set-Content -Encoding ascii (Join-Path $dir "woodpile.toml") @"
[listeners.udp]
enabled = true
addr = "127.0.0.1"
port = 15514
[listeners.tcp]
enabled = true
addr = "127.0.0.1"
port = 15514
[storage]
data_dir = "$d"
[ui]
port = 18514
"@
}

Write-Host "==> woodpile run with the default config creates C:\ProgramData\Woodpile with a protected ACL"
$pd = Join-Path $env:ProgramData "Woodpile"
if (Test-Path $pd) { Die "$pd exists already on this runner" }
$r = Start-Process -FilePath $exe -ArgumentList "run" -PassThru -RedirectStandardOutput (Join-Path $work "run-default.out") -RedirectStandardError (Join-Path $work "run-default.err.out")
if (-not (Wait-UI 8514)) { Die "woodpile run didn't come up" }
if (Broad $pd) { Die "$pd lets Users, Authenticated Users or Everyone in" }
if ((Get-Acl $pd).Sddl -notmatch "D:P") { Die "$pd's ACL isn't protected" }
Ok "a new C:\ProgramData\Woodpile is protected (no Users, Authenticated Users or Everyone)"
Stop-Process -Id $r.Id -Force; $r.WaitForExit(10000) | Out-Null

# C:\ grants Authenticated Users modify and Users read to new folders: a shared folder.
$shared = "C:\wp-shared"
Write-TestConfig $shared
if (-not (Broad $shared)) { Die "the test folder $shared isn't shared to begin with" }
Write-Host "==> under the service, a shared data folder stops the start"
$out = & $exe run --service --config (Join-Path $shared "woodpile.toml") 2>&1 | Out-String
Write-Host $out
if ($LASTEXITCODE -eq 0 -or $out -notmatch "allow-shared-data-dir") { Die "--service started on a shared data folder (exit $LASTEXITCODE)" }
Ok "--service refuses a data folder Users can open, and names --allow-shared-data-dir"
Write-Host "==> --allow-shared-data-dir starts it, with a banner"
$r = Start-Process -FilePath $exe -ArgumentList "run","--service","--allow-shared-data-dir","--config",(Join-Path $shared "woodpile.toml") -PassThru `
    -RedirectStandardOutput (Join-Path $work "run-shared.out") -RedirectStandardError (Join-Path $work "run-shared.err.out")
if (-not (Wait-UI 18514)) { Die "--allow-shared-data-dir didn't start" }
$st = Invoke-RestMethod "http://127.0.0.1:18514/api/status"
if (-not ($st.banners | Where-Object { $_.kind -eq "shared_data_dir" })) { Die "no shared_data_dir banner" }
Ok "with --allow-shared-data-dir it starts and shows the shared_data_dir banner"
Stop-Process -Id $r.Id -Force; $r.WaitForExit(10000) | Out-Null
Write-Host "==> woodpile run (not the service) fixes a shared folder this user owns"
$r = Start-Process -FilePath $exe -ArgumentList "run","--config",(Join-Path $shared "woodpile.toml") -PassThru `
    -RedirectStandardOutput (Join-Path $work "run-fix.out") -RedirectStandardError (Join-Path $work "run-fix.err.out")
if (-not (Wait-UI 18514)) { Die "woodpile run didn't start" }
if (Broad $shared) { Die "woodpile run left $shared shared" }
Ok "woodpile run made a shared folder it owns private"
Stop-Process -Id $r.Id -Force; $r.WaitForExit(10000) | Out-Null

# ---- re-test W1: a folder another user created, with a narrow ACL naming them ----
Write-Host "==> a data folder owned by another user, with a protected ACL naming them, isn't accepted"
New-LocalUser -Name "wpother" -NoPassword -Description "Woodpile smoke test" | Out-Null
$foreign = "C:\wp-foreign"
Write-TestConfig $foreign
icacls $foreign /setowner wpother /T | Out-Null
# On the folder only: its inheritable entries then propagate to the config file
# ((OI)(CI) on a file itself is invalid and would leave it with no entries).
icacls $foreign /inheritance:r /grant:r "wpother:(OI)(CI)F" "${env:USERNAME}:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" | Out-Null
icacls (Join-Path $foreign "woodpile.toml") /reset | Out-Null
$before = (Get-Acl $foreign).Sddl
Write-Host "  $foreign ACL: $before"
if (Broad $foreign) { Die "the test ACL names a broad group" }
$out = & $exe run --service --config (Join-Path $foreign "woodpile.toml") 2>&1 | Out-String
Write-Host $out
if ($out -notmatch "allow-shared-data-dir" -or $out -notmatch "wpother") { Die "--service accepted a folder owned by another user" }
Ok "--service refuses a folder owned by (and open to) another user, and names them"
$r = Start-Process -FilePath $exe -ArgumentList "run","--config",(Join-Path $foreign "woodpile.toml") -PassThru `
    -RedirectStandardOutput (Join-Path $work "run-foreign.out") -RedirectStandardError (Join-Path $work "run-foreign.err.out")
if (-not (Wait-UI 18514)) { Die "woodpile run didn't start on the foreign folder" }
$st = Invoke-RestMethod "http://127.0.0.1:18514/api/status"
$b = $st.banners | Where-Object { $_.kind -eq "shared_data_dir" }
if (-not $b -or $b.text -notmatch "wpother") { Die "no shared_data_dir banner naming the other user" }
if ((Get-Acl $foreign).Sddl -ne $before) { Die "woodpile run changed the ACL of a folder it doesn't own" }
Ok "woodpile run starts on it with a banner naming the other user, and leaves its ACL alone"
Stop-Process -Id $r.Id -Force; $r.WaitForExit(10000) | Out-Null

Copy-Item "$work\*.out" $Logs -ErrorAction SilentlyContinue
Write-Host "`nportable smoke test (Windows): $script:passes checks passed"
# The refused --service start left $LASTEXITCODE at 1, which the runner would report.
exit 0
