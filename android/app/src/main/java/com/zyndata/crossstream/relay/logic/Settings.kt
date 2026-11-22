package com.zyndata.crossstream.relay.logic

import org.json.JSONObject

/** Data limit period (control.Limits cap_period). */
enum class Period(val wire: String) {
    SESSION("session"),
    DAY("day"),
    MONTH("month"),
    ;

    companion object {
        fun of(s: String?): Period = entries.firstOrNull { it.wire == s } ?: MONTH
    }
}

/** What happens over the limit (control.Limits over_cap). */
enum class OverLimit(val wire: String) {
    STOP("stop"),
    THROTTLE("throttle"),
    ;

    companion object {
        fun of(s: String?): OverLimit = entries.firstOrNull { it.wire == s } ?: STOP
    }
}

/** How a hub may use the friend's data (control.Limits max_priority). */
enum class HubUse(val wire: String) {
    NORMAL("normal"),
    WHEN_NEEDED("secondary"),
    BACKUP_ONLY("backup"),
    ;

    companion object {
        fun of(s: String?): HubUse = entries.firstOrNull { it.wire == s } ?: NORMAL
    }
}

/** The friend's relay settings (persisted in DataStore). */
data class RelaySettings(
    /** Data limit per period in MB (decimal, like carrier billing); 0 = unlimited. */
    val capMb: Long = DEFAULT_CAP_MB,
    val period: Period = Period.MONTH,
    /** Day of month the monthly period starts (1-28). */
    val resetDay: Int = 1,
    val overLimit: OverLimit = OverLimit.STOP,
    val throttleKbps: Int = THROTTLE_KBPS,
    /** Stop relaying below this battery percent; 0 = never. */
    val batteryMin: Int = DEFAULT_BATTERY_MIN,
    /** Also stop below batteryMin while charging (default: stop anyway). */
    val batteryStopWhenCharging: Boolean = true,
) {
    val unlimited: Boolean get() = capMb <= 0

    /**
     * control.Limits JSON for the Go core. The app counts the period itself
     * (usedBytes, rounded down to MB) and decides battery stops, so
     * battery_min stays 0 (docs/decisions.md, phase 08).
     */
    fun limitsJson(usedBytes: Long, use: HubUse = HubUse.NORMAL): String {
        val o = JSONObject()
        if (use != HubUse.NORMAL) o.put("max_priority", use.wire)
        if (!unlimited) {
            o.put("cap_mb", capMb)
            o.put("cap_period", period.wire)
            if (period == Period.MONTH) o.put("reset_day", resetDay.coerceIn(1, 28))
            if (usedBytes > 0) o.put("used_mb", usedBytes / BYTES_PER_MB)
            o.put("over_cap", overLimit.wire)
            if (overLimit == OverLimit.THROTTLE) o.put("throttle_kbps", throttleKbps)
        }
        return o.toString()
    }

    companion object {
        const val BYTES_PER_MB = 1_000_000L
        const val DEFAULT_CAP_MB = 2_000L
        const val DEFAULT_BATTERY_MIN = 20
        const val THROTTLE_KBPS = 256

        /** Limit presets in MB (500 MB, 1, 2, 5 GB); custom and unlimited are separate. */
        val PRESETS_MB = listOf(500L, 1_000L, 2_000L, 5_000L)

        /** Suggested daily limit for a monthly plan: 10 % (a common default). */
        fun suggestedDailyMb(monthlyMb: Long): Long = monthlyMb / 10
    }
}
