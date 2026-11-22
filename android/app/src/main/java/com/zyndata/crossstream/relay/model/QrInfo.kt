package com.zyndata.crossstream.relay.model

import org.json.JSONObject

/** What the app needs from a hub's XSR1 code (validated by the Go core's parseQR). */
data class QrInfo(val hub: String, val hpk: String, val ssid: String, val bssid: String, val psk: String) {
    companion object {
        fun fromJson(json: String): QrInfo {
            val o = JSONObject(json)
            return QrInfo(
                hub = o.getString("hub"),
                hpk = o.getString("hpk"),
                ssid = o.optString("ssid"),
                bssid = o.optString("bssid"),
                psk = o.optString("psk"),
            )
        }
    }
}
