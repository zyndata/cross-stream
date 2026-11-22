package com.zyndata.crossstream.relay.model

import java.util.Locale
import org.json.JSONObject

/** Relay state from the Go core's Core.stats() JSON. */
data class RelayStats(
    val running: Boolean = false,
    val connected: Boolean = false,
    val hub: String = "",
    val relayId: String = "",
    val pathId: Int = 0,
    val bytesUp: Long = 0,
    val bytesDown: Long = 0,
    val dropped: Long = 0,
    val proxyFlows: Int = 0,
    val wifi: Boolean = false,
    val cellular: Boolean = false,
    val lastError: String = "",
) {
    /** Bytes relayed over cellular in both directions. */
    val sharedBytes: Long get() = bytesUp + bytesDown

    companion object {
        fun fromJson(json: String): RelayStats {
            val o = JSONObject(json)
            return RelayStats(
                running = o.optBoolean("running"),
                connected = o.optBoolean("connected"),
                hub = o.optString("hub"),
                relayId = o.optString("relay_id"),
                pathId = o.optInt("path_id"),
                bytesUp = o.optLong("bytes_up"),
                bytesDown = o.optLong("bytes_down"),
                dropped = o.optLong("dropped"),
                proxyFlows = o.optInt("proxy_flows"),
                wifi = o.optBoolean("wifi"),
                cellular = o.optBoolean("cellular"),
                lastError = o.optString("last_error"),
            )
        }
    }
}

/** Formats a byte count as megabytes with one decimal, e.g. "12.3 MB". */
fun formatMb(bytes: Long): String = String.format(Locale.ROOT, "%.1f MB", bytes / 1_000_000.0)

/** Formats a byte count as MB below 1 GB and GB with two decimals above, e.g. "2.35 GB". */
fun formatData(bytes: Long): String =
    if (bytes < 999_950_000L) formatMb(bytes) else String.format(Locale.ROOT, "%.2f GB", bytes / 1_000_000_000.0)
