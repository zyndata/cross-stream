# Windows Mobile Hotspot through the WinRT tethering API (run by xs-hubd).
# Inputs (set before this script): $Op = up|down, $Ssid, $Pass, $Band (a|bg),
# $Share (alias of the tunnel adapter to share; "" = none yet), $Restart (1 =
# stop a running hotspot first, so it picks up the shared profile).
# Output (up): key=value lines iface=, mac=, addr=, shared=, band=, warn=.
# Mobile Hotspot does the connection sharing (DHCP, NAT) itself, for the
# profile it was started on; that sharing is not visible to HNetCfg.
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$ext = [System.WindowsRuntimeSystemExtensions].GetMethods()
$asTaskOp = $ext | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and
    $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1' } | Select-Object -First 1
$asTaskAct = $ext | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and
    $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncAction' } | Select-Object -First 1
function Await($op, [Type]$t) {
    $task = $asTaskOp.MakeGenericMethod($t).Invoke($null, @($op)); $task.Wait(-1) | Out-Null; $task.Result
}
function AwaitAction($op) { $asTaskAct.Invoke($null, @($op)).Wait(-1) | Out-Null }
[Windows.Networking.Connectivity.NetworkInformation, Windows.Networking.Connectivity, ContentType = WindowsRuntime] | Out-Null
[Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager, Windows.Networking.NetworkOperators, ContentType = WindowsRuntime] | Out-Null
$result = [Windows.Networking.NetworkOperators.NetworkOperatorTetheringOperationResult]
$ni = [Windows.Networking.Connectivity.NetworkInformation]
$tmType = [Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager]
if ($Op -eq 'parse') { return } # syntax and type-load check (tests)

# Profile: the tunnel's (it appears a few seconds after the adapter), else the
# internet profile, else any profile with an adapter (relays only need the
# hotspot itself until the tunnel exists).
function Profile {
    if ($Share) {
        $a = Get-NetAdapter -Name $Share -ErrorAction SilentlyContinue
        if ($a) {
            $guid = $a.InterfaceGuid.Trim('{}')
            for ($i = 0; $i -lt 30; $i++) {
                $p = $ni::GetConnectionProfiles() | Where-Object { $_.NetworkAdapter -and
                    $_.NetworkAdapter.NetworkAdapterId.ToString() -eq $guid } | Select-Object -First 1
                if ($p) { return $p }
                Start-Sleep -Milliseconds 500
            }
            "warn=Windows offers no connection profile for ${Share}: hotspot clients cannot use the tunnel"
        }
    }
    $p = $ni::GetInternetConnectionProfile()
    if (-not $p) { $p = $ni::GetConnectionProfiles() | Where-Object { $_.NetworkAdapter } | Select-Object -First 1 }
    if (-not $p) { throw "Mobile Hotspot needs a network connection to attach to (any Wi-Fi or Ethernet profile)" }
    $p
}

if ($Op -eq 'down') {
    $p = $ni::GetConnectionProfiles() | Where-Object { $_.NetworkAdapter } | Select-Object -First 1
    if (-not $p) { return }
    $tm = $tmType::CreateFromConnectionProfile($p)
    if ($tm.TetheringOperationalState -ne 'Off') { $r = Await ($tm.StopTetheringAsync()) $result; "stop=$($r.Status)" }
    return
}

# Windows turns Mobile Hotspot off after 5 min without clients by default;
# the hub must stay up for relays that come and go.
$icsKey = 'HKLM:\SYSTEM\CurrentControlSet\Services\icssvc\Settings'
if (-not (Test-Path $icsKey)) { New-Item $icsKey -Force | Out-Null }
New-ItemProperty $icsKey -Name PeerlessTimeoutEnabled -Value 0 -PropertyType DWord -Force | Out-Null

$out = @(Profile)
$out | Where-Object { $_ -is [string] }
$prof = $out | Where-Object { $_ -isnot [string] } | Select-Object -Last 1
$tm = $tmType::CreateFromConnectionProfile($prof)
$cfg = $tm.GetCurrentAccessPointConfiguration()
$changed = ($Restart -eq '1') -or ($cfg.Ssid -cne $Ssid) -or ($cfg.Passphrase -cne $Pass)
$cfg.Ssid = $Ssid
$cfg.Passphrase = $Pass
if ($cfg | Get-Member -Name Band) {
    $want = switch ($Band) { 'a' { 2 } 'bg' { 1 } default { 0 } }
    if ($want -eq 2 -and -not $cfg.IsBandSupported(2)) { $want = 1; "warn=5 GHz not supported by this Wi-Fi adapter, using 2.4 GHz" }
    if ([int]$cfg.Band -ne $want) { $cfg.Band = $want; $changed = $true }
    "band=" + @('auto', 'bg', 'a', '6g')[[int]$cfg.Band]
} else {
    "warn=this Windows version cannot choose the hotspot band"
}
if ($changed -and $tm.TetheringOperationalState -ne 'Off') {
    Await ($tm.StopTetheringAsync()) $result | Out-Null
    Start-Sleep -Seconds 2
}
AwaitAction ($tm.ConfigureAccessPointAsync($cfg))
if ($tm.TetheringOperationalState -ne 'On') {
    # Sharing the tunnel: Internet Connection Sharing hands hotspot clients a
    # DNS server only after a fresh start of its service with the tunnel
    # shared; once another adapter came and went it gave clients none
    # (Lenovo, 2026-11-22). Stopping it here is cheap: tethering starts it again.
    if ($Share -and (Get-NetAdapter -Name $Share -ErrorAction SilentlyContinue)) {
        Stop-Service SharedAccess -Force -ErrorAction SilentlyContinue -WarningAction SilentlyContinue
        "ics=restarted"
    }
    $r = Await ($tm.StartTetheringAsync()) $result
    if ($r.Status -ne 'Success') { throw "Mobile Hotspot did not start: $($r.Status) $($r.AdditionalErrorMessage)" }
}
"shared=$($prof.ProfileName)"
$ip = $null
$na = $null
# The hotspot adapter first shows a 169.254 address; wait for the real one.
for ($i = 0; $i -lt 40 -and -not $ip; $i++) {
    foreach ($a in Get-NetAdapter | Where-Object { $_.InterfaceDescription -like '*Wi-Fi Direct Virtual*' -and $_.Status -eq 'Up' }) {
        $ip = Get-NetIPAddress -InterfaceIndex $a.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue |
            Where-Object { $_.AddressState -eq 'Preferred' -and $_.IPAddress -notlike '169.254.*' } | Select-Object -First 1
        if ($ip) { $na = $a; break }
    }
    if (-not $ip) { Start-Sleep -Milliseconds 500 }
}
if (-not $ip) { throw "the hotspot adapter got no IPv4 address" }
"iface=$($na.Name)"
"mac=$($na.MacAddress)"
"addr=$($ip.IPAddress)/$($ip.PrefixLength)"
