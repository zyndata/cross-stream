package com.zyndata.crossstream.relay.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.longPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import com.zyndata.crossstream.relay.logic.HubUse
import com.zyndata.crossstream.relay.logic.OverLimit
import com.zyndata.crossstream.relay.logic.Period
import com.zyndata.crossstream.relay.logic.RelaySettings
import com.zyndata.crossstream.relay.logic.SavedHub
import com.zyndata.crossstream.relay.logic.ThemeMode
import com.zyndata.crossstream.relay.logic.Usage
import java.io.File
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

/**
 * Persistent app state in one Preferences DataStore: the toggle, settings,
 * saved hubs and the data counter. Hub state dirs (relay key + pairing) live
 * under files/hubs/<id>.
 */
class Repository(private val context: Context, private val store: DataStore<Preferences>) : AppUpdates.Store {

    val enabled: Flow<Boolean> = store.data.map { it[ENABLED] ?: false }.distinctUntilChanged()
    val settings: Flow<RelaySettings> = store.data.map(::settingsOf).distinctUntilChanged()
    val hubs: Flow<List<SavedHub>> = store.data.map { SavedHub.listFromJson(it[HUBS]) }.distinctUntilChanged()
    val usage: Flow<Usage> = store.data.map(::usageOf).distinctUntilChanged()
    val theme: Flow<ThemeMode> = store.data.map { ThemeMode.of(it[THEME]) }.distinctUntilChanged()
    val sessionStart: Flow<Long> = store.data.map { it[SESSION_START] ?: 0L }.distinctUntilChanged()

    /** Check for a new release when the app's screen opens (default on). */
    override val updateAuto: Flow<Boolean> = store.data.map { it[UPDATE_AUTO] ?: true }.distinctUntilChanged()

    suspend fun setUpdateAuto(on: Boolean) {
        store.edit { it[UPDATE_AUTO] = on }
    }

    /** The release tag whose Home banner the user closed. */
    override suspend fun updateDismissed(): String? = store.data.first()[UPDATE_DISMISSED]

    override suspend fun dismissUpdate(tag: String) {
        store.edit { it[UPDATE_DISMISSED] = tag }
    }

    /** Switches relaying on (new session for the per-session limit) or off. */
    suspend fun setEnabled(on: Boolean, nowMs: Long = System.currentTimeMillis()) {
        store.edit { p ->
            if (on && p[ENABLED] != true) p[SESSION_START] = nowMs
            p[ENABLED] = on
        }
    }

    suspend fun setTheme(mode: ThemeMode) {
        store.edit { it[THEME] = mode.key }
    }

    suspend fun updateSettings(f: (RelaySettings) -> RelaySettings) {
        store.edit { p ->
            val s = f(settingsOf(p))
            p[CAP_MB] = s.capMb
            p[PERIOD] = s.period.wire
            p[RESET_DAY] = s.resetDay.coerceIn(1, 28)
            p[OVER_LIMIT] = s.overLimit.wire
            p[BATTERY_MIN] = s.batteryMin.coerceIn(0, 100)
            p[BATTERY_WHEN_CHARGING] = s.batteryStopWhenCharging
        }
    }

    suspend fun updateUsage(f: (Usage) -> Usage) {
        store.edit { p ->
            val u = f(usageOf(p))
            p[USAGE_START] = u.periodStart
            p[USAGE_USED] = u.used
            p[USAGE_ALL] = u.allTime
        }
    }

    /** Adds a hub or replaces the one with the same id (a re-scanned code). */
    suspend fun saveHub(hub: SavedHub) = updateHubs { list ->
        val old = list.firstOrNull { it.id == hub.id }
        list.filter { it.id != hub.id } +
            hub.copy(lastSeen = old?.lastSeen ?: 0, sharedBytes = old?.sharedBytes ?: 0, use = old?.use ?: hub.use,
                preferred = old?.preferred ?: hub.preferred, label = old?.label ?: hub.label)
    }

    /** Gives a hub the friend's own name ("" = back to the hub's name); only this phone sees it. */
    suspend fun renameHub(id: String, label: String) = updateHubs { list ->
        list.map { if (it.id == id) it.copy(label = SavedHub.cleanLabel(label)) else it }
    }

    /** Makes id the chosen hub ("" = choose automatically); at most one hub is chosen. */
    suspend fun setPreferredHub(id: String) = updateHubs { list ->
        list.map { it.copy(preferred = it.id == id) }
    }

    /** Sets how a hub may use this phone's data (sent live to the hub as limits.max_priority). */
    suspend fun setHubUse(id: String, use: HubUse) = updateHubs { list ->
        list.map { if (it.id == id) it.copy(use = use) else it }
    }

    /** Records a connected session: last seen now, shared bytes added. */
    suspend fun touchHub(id: String, nowMs: Long, addBytes: Long) = updateHubs { list ->
        list.map { if (it.id == id) it.copy(lastSeen = nowMs, sharedBytes = it.sharedBytes + addBytes) else it }
    }

    /** Forgets a hub: deletes its entry and its state dir (relay key, pairing). */
    suspend fun forgetHub(id: String) {
        updateHubs { list -> list.filter { it.id != id } }
        hubDir(id).deleteRecursively()
    }

    suspend fun hubsNow(): List<SavedHub> = hubs.first()

    fun hubDir(id: String): File = File(context.filesDir, "hubs/$id")

    fun logDir(): File = File(context.filesDir, "logs").apply { mkdirs() }

    /**
     * Phase 07 kept one hub: its code in SharedPreferences "hub" and its state
     * in files/relay. Moves it to a saved hub with its own dir (keeps pairing).
     */
    suspend fun migrateLegacy(idOf: (String) -> SavedHub?) {
        val prefs = context.getSharedPreferences("hub", Context.MODE_PRIVATE)
        val qr = prefs.getString("qr", null) ?: return
        val hub = idOf(qr)
        if (hub != null) {
            val old = File(context.filesDir, "relay")
            val dir = hubDir(hub.id)
            if (old.isDirectory && !dir.exists()) {
                dir.parentFile?.mkdirs()
                if (!old.renameTo(dir)) old.copyRecursively(dir, overwrite = false)
            }
            saveHub(hub)
        }
        prefs.edit().clear().apply()
    }

    private suspend fun updateHubs(f: (List<SavedHub>) -> List<SavedHub>) {
        store.edit { p -> p[HUBS] = SavedHub.listToJson(f(SavedHub.listFromJson(p[HUBS]))) }
    }

    private fun settingsOf(p: Preferences) = RelaySettings(
        capMb = p[CAP_MB] ?: RelaySettings.DEFAULT_CAP_MB,
        period = Period.of(p[PERIOD]),
        resetDay = p[RESET_DAY] ?: 1,
        overLimit = OverLimit.of(p[OVER_LIMIT]),
        batteryMin = p[BATTERY_MIN] ?: RelaySettings.DEFAULT_BATTERY_MIN,
        batteryStopWhenCharging = p[BATTERY_WHEN_CHARGING] ?: true,
    )

    private fun usageOf(p: Preferences) = Usage(p[USAGE_START] ?: 0L, p[USAGE_USED] ?: 0L, p[USAGE_ALL] ?: 0L)

    private companion object {
        val ENABLED = booleanPreferencesKey("enabled")
        val SESSION_START = longPreferencesKey("session_start")
        val CAP_MB = longPreferencesKey("cap_mb")
        val PERIOD = stringPreferencesKey("period")
        val RESET_DAY = intPreferencesKey("reset_day")
        val OVER_LIMIT = stringPreferencesKey("over_limit")
        val BATTERY_MIN = intPreferencesKey("battery_min")
        val BATTERY_WHEN_CHARGING = booleanPreferencesKey("battery_stop_when_charging")
        val HUBS = stringPreferencesKey("hubs")
        val THEME = stringPreferencesKey("theme")
        val USAGE_START = longPreferencesKey("usage_period_start")
        val USAGE_USED = longPreferencesKey("usage_used")
        val USAGE_ALL = longPreferencesKey("usage_all_time")
        val UPDATE_AUTO = booleanPreferencesKey("update_auto")
        val UPDATE_DISMISSED = stringPreferencesKey("update_dismissed")
    }
}
