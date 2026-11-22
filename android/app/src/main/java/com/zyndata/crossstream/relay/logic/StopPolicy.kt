package com.zyndata.crossstream.relay.logic

/** What the phone reports for the stop decisions. */
data class DeviceState(
    val battery: Int = 100,
    val charging: Boolean = false,
    /** Cellular is VALIDATED and not behind a captive portal. */
    val cellularValidated: Boolean = true,
    /** Since when (epoch ms) cellular has not been validated; null while it is. */
    val cellularBadSince: Long? = null,
)

/**
 * Stop and resume rules. Reasons are control.Leaving* values (also passed to
 * Core.stopWith): "limit", "battery", "no_data".
 */
object StopPolicy {
    const val LIMIT = "limit"
    const val BATTERY = "battery"
    const val NO_DATA = "no_data"

    /** Cellular may be unvalidated this long before the relay leaves (re-validation, handover). */
    const val NO_DATA_GRACE_MS = 30_000L

    /** Battery must be this many percent above the threshold to resume. */
    const val BATTERY_HYSTERESIS = 5

    /** The reason the relay must stop now, or null. */
    fun stopReason(s: RelaySettings, usage: Usage, dev: DeviceState, nowMs: Long): String? = when {
        usage.overLimit(s) && s.overLimit == OverLimit.STOP -> LIMIT
        batteryLow(s, dev, s.batteryMin) -> BATTERY
        dev.cellularBadSince != null && nowMs - dev.cellularBadSince >= NO_DATA_GRACE_MS -> NO_DATA
        else -> null
    }

    /** Whether a relay paused for reason may start again. */
    fun canResume(reason: String, s: RelaySettings, usage: Usage, dev: DeviceState, nowMs: Long): Boolean {
        if (stopReason(s, usage, dev, nowMs) != null) return false
        return when (reason) {
            BATTERY -> !batteryLow(s, dev, minOf(s.batteryMin + BATTERY_HYSTERESIS, 100))
            NO_DATA -> dev.cellularValidated
            else -> true
        }
    }

    private fun batteryLow(s: RelaySettings, dev: DeviceState, threshold: Int): Boolean =
        s.batteryMin > 0 && dev.battery < threshold && (s.batteryStopWhenCharging || !dev.charging)
}
