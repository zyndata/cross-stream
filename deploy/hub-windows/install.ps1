# Installs (or with -Uninstall removes) the CrossStream hub on a Windows laptop:
# binaries and wintun.dll in "C:\Program Files\CrossStream", the state directory
# C:\ProgramData\CrossStream (Administrators/SYSTEM only), local group
# "crossstream" (the installing user is added; members may use xs-hubctl and the
# GUI and start/stop the service), the CrossStreamHub service (manual start) and
# Windows Firewall rules for xs-hubd on the hotspot. It does not start the service.
# Run from an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File deploy\hub-windows\install.ps1 [-Bin .\bin\windows] [-Uninstall]
#   (from an unpacked release zip: install.ps1 next to the binaries, no -Bin needed)
param([string]$Bin = "", [switch]$Uninstall)

$ErrorActionPreference = "Stop"
if (-not $Bin) {
    $Bin = if (Test-Path (Join-Path $PSScriptRoot "xs-hubd.exe")) { $PSScriptRoot } else { Join-Path $PSScriptRoot "..\..\bin\windows" }
}
$dest = Join-Path $env:ProgramFiles "CrossStream"
$state = Join-Path $env:ProgramData "CrossStream"
$svcName = "CrossStreamHub"
$group = "crossstream"
$files = "xs-hubd.exe", "xs-hubctl.exe", "wintun.dll"

$me = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $me.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "run from an elevated PowerShell (Run as administrator)"
}

if ($Uninstall) {
    if (Get-Service $svcName -ErrorAction SilentlyContinue) {
        Stop-Service $svcName -Force -ErrorAction SilentlyContinue
        sc.exe delete $svcName | Out-Null
    }
    Get-NetFirewallRule -Group "CrossStream" -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    Remove-Item (Join-Path $env:ProgramData "Microsoft\Windows\Start Menu\Programs\CrossStream Hub.lnk") -ErrorAction SilentlyContinue
    Remove-Item $dest -Recurse -Force -ErrorAction SilentlyContinue
    "removed the CrossStreamHub service, firewall rules and $dest (kept $state and group $group)"
    return
}

foreach ($f in $files) {
    if (-not (Test-Path (Join-Path $Bin $f))) { throw "$Bin\$f not found: run make hub-windows first" }
}
$build = [Environment]::OSVersion.Version.Build
if ($build -lt 17763) { throw "Windows 10 1809 or newer is needed (AF_UNIX sockets, Mobile Hotspot API); this is build $build" }

if (Get-Service $svcName -ErrorAction SilentlyContinue) { Stop-Service $svcName -Force -ErrorAction SilentlyContinue }
New-Item -ItemType Directory -Force $dest | Out-Null
foreach ($f in $files) { Copy-Item (Join-Path $Bin $f) $dest -Force }
"copied $($files -join ', ') to $dest"
# Optional desktop app (built on Windows with make gui) + Start menu shortcut.
$gui = Join-Path $Bin "xs-hub-gui.exe"
if (Test-Path $gui) {
    Copy-Item $gui $dest -Force
    $lnk = Join-Path $env:ProgramData "Microsoft\Windows\Start Menu\Programs\CrossStream Hub.lnk"
    $sh = (New-Object -ComObject WScript.Shell).CreateShortcut($lnk)
    $sh.TargetPath = Join-Path $dest "xs-hub-gui.exe"
    $sh.Save()
    "GUI: Start menu > CrossStream Hub"
}

# Group for the GUI and xs-hubctl (the socket ACL and the service ACL name it).
if (-not (Get-LocalGroup $group -ErrorAction SilentlyContinue)) {
    New-LocalGroup $group -Description "May use the CrossStream hub (GUI, xs-hubctl)" | Out-Null
}
$user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
if (-not (Get-LocalGroupMember $group -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $user })) {
    Add-LocalGroupMember $group -Member $user
    "added $user to group $group (sign out and in again to use the GUI and xs-hubctl)"
}
$gsid = (Get-LocalGroup $group).SID.Value

# State directory: xs-hubd also enforces this ACL at every start.
New-Item -ItemType Directory -Force $state | Out-Null
$acl = New-Object Security.AccessControl.DirectorySecurity
$acl.SetSecurityDescriptorSddlForm("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
Set-Acl $state $acl

# Service: LocalSystem, manual start; the group may query, start and stop it.
$exe = Join-Path $dest "xs-hubd.exe"
if (-not (Get-Service $svcName -ErrorAction SilentlyContinue)) {
    New-Service -Name $svcName -BinaryPathName "`"$exe`" -socket-group $group" -DisplayName "CrossStream hub" `
        -Description "Bonds phone connections and shares them over Mobile Hotspot" -StartupType Manual | Out-Null
} else {
    sc.exe config $svcName binPath= "`"$exe`" -socket-group $group" | Out-Null
}
sc.exe failure $svcName reset= 86400 actions= restart/5000/restart/30000// | Out-Null
$sddl = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWRPWPDTLOCRRC;;;$gsid)"
sc.exe sdset $svcName $sddl | Out-Null
"service $svcName installed (manual start; group $group may start and stop it)"

# Firewall: relays and clients reach xs-hubd on the hotspot (control 51901/tcp,
# data 51902/udp, DNS 53). The hotspot network is "Public" to Windows.
Get-NetFirewallRule -Group "CrossStream" -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName "CrossStream hub (TCP)" -Group "CrossStream" -Direction Inbound -Program $exe `
    -Protocol TCP -LocalPort 51901, 53 -Profile Any -Action Allow | Out-Null
New-NetFirewallRule -DisplayName "CrossStream hub (UDP)" -Group "CrossStream" -Direction Inbound -Program $exe `
    -Protocol UDP -LocalPort 51902, 53 -Profile Any -Action Allow | Out-Null
"firewall: xs-hubd may receive 51901/tcp, 51902/udp and DNS"

"installed. Start: Start-Service $svcName (or from the GUI); at boot: Set-Service $svcName -StartupType Automatic"
"Mobile Hotspot must be available on this laptop (Settings > Network > Mobile hotspot)."
