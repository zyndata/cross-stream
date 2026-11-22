package com.zyndata.crossstream.relay.logic

import java.time.Instant
import java.time.ZoneId

/**
 * Relayed bytes (cellular, both directions) counted against the data limit.
 * periodStart is when the current limit period began (epoch ms).
 */
data class Usage(val periodStart: Long = 0, val used: Long = 0, val allTime: Long = 0) {

    /** Starts a new period (used = 0) if the current one has ended. */
    fun roll(s: RelaySettings, nowMs: Long, sessionStartMs: Long, zone: ZoneId): Usage {
        val ps = periodStart(s, nowMs, sessionStartMs, zone)
        return if (ps > periodStart) copy(periodStart = ps, used = 0) else this
    }

    /** Adds delta bytes in the period containing nowMs. */
    fun add(delta: Long, s: RelaySettings, nowMs: Long, sessionStartMs: Long, zone: ZoneId): Usage {
        val r = roll(s, nowMs, sessionStartMs, zone)
        if (delta <= 0) return r
        return r.copy(used = r.used + delta, allTime = r.allTime + delta)
    }

    /** The limit is reached (in any over-limit mode). */
    fun overLimit(s: RelaySettings): Boolean = !s.unlimited && used >= s.capMb * RelaySettings.BYTES_PER_MB

    /** Counter reset by the user: a new period starts now. */
    fun reset(nowMs: Long): Usage = copy(periodStart = nowMs, used = 0)

    companion object {
        /**
         * When the period containing nowMs began (same rules as the Go meter):
         * session = when the toggle was switched on; day = local midnight;
         * month = 00:00 on the most recent reset day (1-28).
         */
        fun periodStart(s: RelaySettings, nowMs: Long, sessionStartMs: Long, zone: ZoneId): Long {
            val today = Instant.ofEpochMilli(nowMs).atZone(zone).toLocalDate()
            return when (s.period) {
                Period.SESSION -> sessionStartMs
                Period.DAY -> today.atStartOfDay(zone).toInstant().toEpochMilli()
                Period.MONTH -> {
                    val rd = s.resetDay.coerceIn(1, 28)
                    val month = if (today.dayOfMonth < rd) today.minusMonths(1) else today
                    month.withDayOfMonth(rd).atStartOfDay(zone).toInstant().toEpochMilli()
                }
            }
        }
    }
}
