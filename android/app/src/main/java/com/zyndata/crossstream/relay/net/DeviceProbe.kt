package com.zyndata.crossstream.relay.net

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.PackageManager
import android.location.LocationManager
import android.net.wifi.WifiManager
import android.os.BatteryManager
import android.telephony.CellInfo
import android.telephony.CellSignalStrength
import android.telephony.CellSignalStrengthGsm
import android.telephony.CellSignalStrengthLte
import android.telephony.CellSignalStrengthNr
import android.telephony.CellSignalStrengthWcdma
import android.telephony.TelephonyManager
import com.zyndata.crossstream.relay.logic.HubPicker
import com.zyndata.crossstream.relay.logic.SeenAp

/** Battery, cellular signal and Wi-Fi scan results, read on demand (no listeners, no wake-ups). */
class DeviceProbe(private val context: Context) {
    private val location = context.getSystemService(LocationManager::class.java)
    private val battery = context.getSystemService(BatteryManager::class.java)
    private val telephony = context.getSystemService(TelephonyManager::class.java)
    private val wifi = context.getSystemService(WifiManager::class.java)

    fun batteryPercent(): Int = battery.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY)

    fun charging(): Boolean = battery.isCharging

    fun carrier(): String = telephony?.networkOperatorName.orEmpty()

    /** Cellular signal in dBm and net type ("nr", "lte", "wcdma", "gsm"); (0, "") if unknown. */
    fun cellular(): Pair<Int, String> {
        val all = runCatching { telephony?.signalStrength?.cellSignalStrengths }.getOrNull().orEmpty()
        val best = all.firstOrNull { it is CellSignalStrengthNr } ?: all.firstOrNull() ?: return 0 to ""
        val dbm = best.dbm.takeIf { it != CellInfo.UNAVAILABLE } ?: 0
        return dbm to typeOf(best)
    }

    /**
     * Asks for a fresh scan; false when Android refused it (an app may scan 4 times in 2 min,
     * or no permission): the results then stay those of the last scan.
     */
    @Suppress("DEPRECATION") // startScan is deprecated but still the only way to request a scan
    fun requestScan(): Boolean = runCatching { wifi.startScan() }.getOrDefault(false)

    /** Whether Wi-Fi is on (scan results may still come with it off: "scanning always available"). */
    fun wifiOn(): Boolean = runCatching { wifi.isWifiEnabled }.getOrDefault(true)

    /**
     * Last scan results, or null when the app cannot scan (no location permission or location
     * off: Android then returns an empty list). An empty list means no AP in range.
     */
    @SuppressLint("MissingPermission") // checked by the caller's permission flow; SecurityException → null
    fun scanResults(): List<SeenAp>? = runCatching {
        HubPicker.latestScan(
            wifi.scanResults.map { r ->
                @Suppress("DEPRECATION") // wifiSsid needs API 33; SSID is fine for matching
                SeenAp(r.BSSID.orEmpty(), r.SSID.orEmpty().removeSurrounding("\""), r.level, r.timestamp)
            },
        )
    }.getOrNull()?.takeIf { it.isNotEmpty() || canScan() }

    /**
     * Whether an empty scan can be trusted as "nothing in range". getScanResults still needs
     * location (permission and location on) on every version: with only NEARBY_WIFI_DEVICES an
     * Android 17 S23 returned an empty list ("no location permission") and the relay searched
     * forever (phase 18). An untrusted empty scan is null: the picker joins the most recently
     * used hub and the Wi-Fi request finds it by SSID/BSSID itself.
     */
    private fun canScan(): Boolean =
        granted(Manifest.permission.ACCESS_FINE_LOCATION) && location?.isLocationEnabled == true

    private fun granted(permission: String) = context.checkSelfPermission(permission) == PackageManager.PERMISSION_GRANTED

    private fun typeOf(s: CellSignalStrength): String = when (s) {
        is CellSignalStrengthNr -> "nr"
        is CellSignalStrengthLte -> "lte"
        is CellSignalStrengthWcdma -> "wcdma"
        is CellSignalStrengthGsm -> "gsm"
        else -> "other"
    }
}
