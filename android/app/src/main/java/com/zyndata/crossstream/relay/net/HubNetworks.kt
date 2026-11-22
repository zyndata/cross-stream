package com.zyndata.crossstream.relay.net

import android.content.Context
import android.net.ConnectivityManager
import android.net.MacAddress
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiNetworkSpecifier
import android.util.Log
import com.zyndata.crossstream.relay.model.QrInfo

/**
 * Requests the two networks the relay needs and reports their handles
 * (Network.getNetworkHandle(), 0 = lost). Kotlin never binds sockets: the Go
 * core binds each socket to the reported network (android_setsocknetwork).
 *
 * - cellular: requestNetwork(TRANSPORT_CELLULAR + INTERNET) keeps mobile data
 *   up while the phone is joined to the hub's Wi-Fi (which has no internet);
 * - hub Wi-Fi: WifiNetworkSpecifier with exact SSID + BSSID + PSK. Android
 *   asks the user once and remembers the approval for that exact network.
 */
class HubNetworks(context: Context, private val listener: Listener) {
    interface Listener {
        /** kind is "wifi" or "cellular"; handle 0 = lost. */
        fun onNetwork(kind: String, handle: Long)

        /** Cellular has VALIDATED internet (false: no data / captive portal). */
        fun onCellularValidated(ok: Boolean)

        /** RSSI of the hub Wi-Fi link in dBm. */
        fun onWifiRssi(rssi: Int)

        /** The user declined the Wi-Fi request or the hub network was not found. */
        fun onWifiUnavailable()
    }

    private val cm = context.getSystemService(ConnectivityManager::class.java)
    private var cellCallback: ConnectivityManager.NetworkCallback? = null
    private var wifiCallback: ConnectivityManager.NetworkCallback? = null

    /** Keeps mobile data up (also while paused for no_data, to see it validate again). */
    fun startCellular() {
        if (cellCallback != null) return
        val cellReq = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        cellCallback = callback(KIND_CELLULAR).also { cm.requestNetwork(cellReq, it) }
    }

    /** Requests the hub's Wi-Fi (a previously approved network connects without a dialog). */
    fun startWifi(hub: QrInfo) {
        stopWifi()
        val spec = WifiNetworkSpecifier.Builder()
            .setSsid(hub.ssid)
            .setWpa2Passphrase(hub.psk)
            .apply { if (hub.bssid.isNotEmpty()) setBssid(MacAddress.fromString(hub.bssid)) }
            .build()
        val wifiReq = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .removeCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .setNetworkSpecifier(spec)
            .build()
        wifiCallback = callback(KIND_WIFI).also { cm.requestNetwork(wifiReq, it, WIFI_TIMEOUT_MS) }
    }

    fun stopWifi() {
        wifiCallback?.let { runCatching { cm.unregisterNetworkCallback(it) } }
        wifiCallback = null
    }

    fun stop() {
        stopWifi()
        cellCallback?.let { runCatching { cm.unregisterNetworkCallback(it) } }
        cellCallback = null
    }

    private fun callback(kind: String) = object : ConnectivityManager.NetworkCallback() {
        private var current: Network? = null

        override fun onAvailable(network: Network) {
            Log.i(TAG, "$kind network available")
            current = network
            listener.onNetwork(kind, network.networkHandle)
        }

        override fun onLost(network: Network) {
            if (network != current) return
            if (kind == KIND_CELLULAR) listener.onCellularValidated(false)
            Log.i(TAG, "$kind network lost")
            current = null
            listener.onNetwork(kind, 0)
        }

        override fun onUnavailable() {
            Log.w(TAG, "$kind network unavailable")
            if (kind == KIND_WIFI) listener.onWifiUnavailable()
        }

        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
            if (kind == KIND_CELLULAR) {
                listener.onCellularValidated(
                    caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) &&
                        !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_CAPTIVE_PORTAL),
                )
            } else {
                listener.onWifiRssi(caps.signalStrength)
            }
        }
    }

    companion object {
        const val KIND_WIFI = "wifi"
        const val KIND_CELLULAR = "cellular"

        /** onUnavailable if the hub Wi-Fi is not joined within this time (absent, or the dialog declined). */
        const val WIFI_TIMEOUT_MS = 45_000
        private const val TAG = "HubNetworks"
    }
}
