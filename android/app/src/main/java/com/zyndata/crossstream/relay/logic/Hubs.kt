package com.zyndata.crossstream.relay.logic

import java.security.MessageDigest
import org.json.JSONArray
import org.json.JSONObject

/** A hub the friend has paired with (one state dir per hub: files/hubs/<id>). */
data class SavedHub(
    val id: String,
    val name: String,
    val ssid: String,
    val bssid: String,
    /** The XSR1 code: hub address, key, Wi-Fi password, pair token. */
    val qr: String,
    /** Last time the relay was connected to this hub (epoch ms, 0 = never). */
    val lastSeen: Long = 0,
    /** Bytes relayed for this hub, all time. */
    val sharedBytes: Long = 0,
    /** How this hub may use the friend's data ("How can this hub use my data"). */
    val use: HubUse = HubUse.NORMAL,
    /** The friend chose this hub: joined whenever it is available (at most one). */
    val preferred: Boolean = false,
    /** The friend's own name for this hub ("" = the hub's name from its code). */
    val label: String = "",
) {
    /** The name the app shows: the friend's label, else the hub's own name. */
    val shownName: String get() = label.ifBlank { name }

    fun toJson(): JSONObject = JSONObject()
        .put("id", id).put("name", name).put("ssid", ssid).put("bssid", bssid).put("qr", qr)
        .put("last_seen", lastSeen).put("shared", sharedBytes).put("use", use.wire).put("preferred", preferred)
        .put("label", label)

    companion object {
        fun fromJson(o: JSONObject) = SavedHub(
            id = o.getString("id"),
            name = o.optString("name"),
            ssid = o.optString("ssid"),
            bssid = o.optString("bssid"),
            qr = o.getString("qr"),
            lastSeen = o.optLong("last_seen"),
            sharedBytes = o.optLong("shared"),
            use = HubUse.of(o.optString("use")),
            preferred = o.optBoolean("preferred"),
            label = o.optString("label"),
        )

        /** Longest label the friend can give a hub. */
        const val LABEL_MAX = 32

        /** A label as saved: trimmed, at most LABEL_MAX characters ("" = the hub's own name). */
        fun cleanLabel(s: String): String = s.trim().take(LABEL_MAX).trim()

        fun listToJson(hubs: List<SavedHub>): String = JSONArray(hubs.map { it.toJson() }).toString()

        fun listFromJson(json: String?): List<SavedHub> {
            if (json.isNullOrEmpty()) return emptyList()
            val a = runCatching { JSONArray(json) }.getOrNull() ?: return emptyList()
            return (0 until a.length()).mapNotNull { i -> runCatching { fromJson(a.getJSONObject(i)) }.getOrNull() }
        }

        /** Stable id of a hub: first 16 hex chars of SHA-256 of its public key (hpk). */
        fun idOf(hpk: String): String =
            MessageDigest.getInstance("SHA-256").digest(hpk.toByteArray()).take(8)
                .joinToString("") { "%02x".format(it) }
    }
}

/** A Wi-Fi network seen in scan results; seenUs = when it was last seen (µs since boot, 0 = unknown). */
data class SeenAp(val bssid: String, val ssid: String, val rssi: Int, val seenUs: Long = 0)

/** Hub state on the home screen. */
/** Hub state on the home screen; UNKNOWN = the phone cannot scan (no location permission). */
enum class HubState { CONNECTED, CONNECTING, IN_RANGE, AWAY, UNKNOWN }

/** Picks the hub to join and paces the search. */
object HubPicker {
    /** Search waits between scans: 5, 10, 20, 40, then 60 s. */
    val BACKOFF_MS = listOf(5_000L, 10_000L, 20_000L, 40_000L, 60_000L)

    /** A hub that declined Wi-Fi or rejected the relay is skipped this long. */
    const val COOLDOWN_MS = 5 * 60_000L

    /** A hub whose Wi-Fi was simply not found (the request timed out) is skipped this long. */
    const val NOT_FOUND_COOLDOWN_MS = 30_000L

    /**
     * Cooldown after Android reported the hub's Wi-Fi unavailable elapsedMs after the request:
     * at the request timeout it was not found (the hub may come back any moment: short), earlier
     * the friend declined the system dialog (long). Phase 19: a hub restarted while the phone
     * could not scan in the background waited 5 min.
     */
    fun unavailableCooldown(elapsedMs: Long, requestTimeoutMs: Long): Long =
        if (elapsedMs >= requestTimeoutMs - NOT_FOUND_SLACK_MS) NOT_FOUND_COOLDOWN_MS else COOLDOWN_MS

    private const val NOT_FOUND_SLACK_MS = 5_000L

    fun backoff(attempt: Int): Long = BACKOFF_MS[attempt.coerceIn(0, BACKOFF_MS.size - 1)]

    /** The saved hub matching ap (BSSID, or SSID for a hub whose code had no BSSID). */
    fun matches(hub: SavedHub, ap: SeenAp): Boolean =
        if (hub.bssid.isNotEmpty()) hub.bssid.equals(ap.bssid, ignoreCase = true) else hub.ssid == ap.ssid

    /**
     * Whether a session with current should end to join the chosen hub instead: one is chosen,
     * it is another hub, not cooling down and (when the scan is known) in range.
     */
    fun switchTo(hubs: List<SavedHub>, current: String, scan: List<SeenAp>?, cooldownUntil: Map<String, Long>, nowMs: Long): Boolean {
        val chosen = hubs.firstOrNull { it.preferred } ?: return false
        return chosen.id != current && (cooldownUntil[chosen.id] ?: 0) <= nowMs &&
            (scan == null || scan.any { matches(chosen, it) })
    }

    /**
     * The scan a running session judges the chosen hub by: newer results replace it, but an
     * empty list (no scan permission in the background) is "unknown", not "nothing in range",
     * so it keeps the last real scan (the session kept the scan of its start, so a chosen hub
     * that came into range later was never joined, 2026-11-26).
     */
    fun sessionScan(known: List<SeenAp>?, fresh: List<SeenAp>?): List<SeenAp>? =
        fresh?.takeIf { it.isNotEmpty() } ?: known

    /**
     * The chosen hub the friend can switch to by hand ("Switch now"): the relay shares with
     * another hub; null when nothing is chosen or the session is already with the chosen hub.
     */
    fun switchOffer(hubs: List<SavedHub>, current: String, inSession: Boolean): SavedHub? {
        if (!inSession || current.isEmpty()) return null
        return hubs.firstOrNull { it.preferred }?.takeIf { it.id != current }
    }

    /** Results older than the newest by more than this are from earlier scans. */
    const val STALE_US = 10_000_000L

    /**
     * The networks of the latest scan: Android's result list also keeps networks from earlier
     * scans for a while, so a hotspot that was just switched off stayed "in range" (phase 18).
     */
    fun latestScan(aps: List<SeenAp>): List<SeenAp> {
        val newest = aps.maxOfOrNull { it.seenUs } ?: return aps
        return aps.filter { it.seenUs == 0L || it.seenUs >= newest - STALE_US }
    }

    /**
     * Whether new scan results should wake a searching relay at once: a saved hub that is not
     * cooling down is visible (phase 19; otherwise the search waits up to 60 s).
     */
    fun wakeFor(hubs: List<SavedHub>, scan: List<SeenAp>?, cooldownUntil: Map<String, Long>, nowMs: Long): Boolean =
        scan != null && pick(hubs, scan, cooldownUntil, nowMs) != null

    /** Ids of saved hubs visible in scan. */
    fun inRange(hubs: List<SavedHub>, scan: List<SeenAp>): Set<String> =
        hubs.filter { h -> scan.any { matches(h, it) } }.map { it.id }.toSet()

    /**
     * The hub to join: the hub the friend chose when it is visible (or the scan is unknown),
     * else the strongest visible saved hub; hubs cooling down are skipped.
     * scan == null (no scan permission or results): the most recently used hub.
     */
    fun pick(hubs: List<SavedHub>, scan: List<SeenAp>?, cooldownUntil: Map<String, Long>, nowMs: Long): SavedHub? {
        val ok = hubs.filter { (cooldownUntil[it.id] ?: 0) <= nowMs }
        val chosen = ok.firstOrNull { it.preferred }
        if (chosen != null && (scan == null || scan.any { matches(chosen, it) })) return chosen
        if (scan == null) return ok.maxByOrNull { it.lastSeen }
        return ok.mapNotNull { h -> scan.filter { matches(h, it) }.maxOfOrNull { it.rssi }?.let { h to it } }
            .maxByOrNull { it.second }?.first
    }
}
