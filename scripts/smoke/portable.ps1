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
if ($banner -notmatch "netsh advfirewall firewall add rule") { Die "no netsh command for the missing rule" }
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

Write-Host "==> close the console window (CTRL_CLOSE_EVENT)"
Start-Sleep 1
$t0 = Get-Date
$how = & (Resolve-Path $ConsoleClose) $p.Id
$cc = $LASTEXITCODE
Write-Host "consoleclose: $how (exit $cc)"
if ($cc -eq 3) { Die "this session's consoles are pseudo consoles (ConPTY); a console close can't be simulated here" }
if ($cc -ne 0) { Die "couldn't close the console window" }
if (-not $p.WaitForExit(10000)) { Die "still running 10 s after the console window closed ($how)" }
$took = ((Get-Date) - $t0).TotalSeconds
$final = Get-Content -Raw $out
Write-Host $final.Substring([Math]::Max(0, $final.Length - 300))
if ($final -notmatch "Woodpile stopped; everything is saved") { Die "no clean stop after the console closed" }
if ($took -gt 5) { Die "stopping took $took s; Windows allows about 5 s" }
Ok ("closing the console window stopped it cleanly in {0:N1} s" -f $took)
$log = Get-Content -Raw (Join-Path $data "woodpile.log")
if ($log -notmatch "stopped cleanly") { Die "woodpile.log has no clean stop" }
& (Resolve-Path $VerifySeq) -data $data -run $run -n 10000
if ($LASTEXITCODE -ne 0) { Die "messages lost at the close" }
Ok "10,000 of 10,000 still on disk after the close"
Copy-Item "$work\*.out" $Logs -ErrorAction SilentlyContinue
Write-Host "`nportable smoke test (Windows): $script:passes checks passed"
