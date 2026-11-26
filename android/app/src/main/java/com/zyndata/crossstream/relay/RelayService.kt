package com.zyndata.crossstream.relay

import android.annotation.SuppressLint
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.PowerManager
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.lifecycleScope
import com.zyndata.crossstream.core.xscore.Callback
import com.zyndata.crossstream.core.xscore.Core
import com.zyndata.crossstream.core.xscore.Xscore
import com.zyndata.crossstream.relay.logic.DeviceState
import com.zyndata.crossstream.relay.logic.HubPicker
import com.zyndata.crossstream.relay.logic.HubUse
import com.zyndata.crossstream.relay.logic.RelaySettings
import com.zyndata.crossstream.relay.logic.SavedHub
import com.zyndata.crossstream.relay.logic.SeenAp
import com.zyndata.crossstream.relay.logic.StopPolicy
import com.zyndata.crossstream.relay.model.Phase
import com.zyndata.crossstream.relay.model.QrInfo
import com.zyndata.crossstream.relay.model.RelayStateHolder
import com.zyndata.crossstream.relay.model.RelayStats
import com.zyndata.crossstream.relay.model.RelayUiState
import com.zyndata.crossstream.relay.net.DeviceProbe
import com.zyndata.crossstream.relay.net.HubNetworks
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull

/**
 * Foreground service (type specialUse) that runs while the toggle is ON:
 * searches for saved hubs, relays through the Go core, pauses on a limit /
 * low battery / no mobile data and resumes by itself. Wake-ups per state are
 * listed in docs/decisions.md (phase 08).
 */
class RelayService : LifecycleService() {
    private lateinit var app: RelayApplication
    private lateinit var probe: DeviceProbe
    private lateinit var networks: HubNetworks
    private lateinit var notes: RelayNotifications
    private var core: Core? = null
    private var main: Job? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null

    private val pokes = Channel<Unit>(Channel.CONFLATED)
    /** A Wi-Fi scan finished (ours or the system's). */
    private val scans = Channel<Unit>(Channel.CONFLATED)
    private val stopEvents = Channel<Pair<String, String>>(Channel.UNLIMITED)
    private val cooldown = mutableMapOf<String, Long>()

    @Volatile private var settings = RelaySettings()
    @Volatile private var hubs = emptyList<SavedHub>()
    /** The latest scan (null = unknown): whether a chosen hub is in range. */
    @Volatile private var lastScan: List<SeenAp>? = null
    /** "Switch now" pressed: the session leaves for the chosen hub. */
    @Volatile private var switchNow = false
    /** The next search joins the chosen hub without waiting for it in a scan. */
    private var forceChosen = false
    @Volatile private var sessionStart = 0L
    @Volatile private var wifiHandle = 0L
    @Volatile private var wifiUnavailable = false
    @Volatile private var wifiRssi = 0
    @Volatile private var cellValidated = true
    @Volatile private var cellBadSince: Long? = null

    override fun onCreate() {
        super.onCreate()
        app = RelayApplication.of(this)
        probe = DeviceProbe(this)
        notes = RelayNotifications(this)
        networks = HubNetworks(this, networkListener)
        lifecycleScope.launch { app.repo.settings.collect { settings = it; pokes.trySend(Unit) } }
        lifecycleScope.launch {
            app.repo.hubs.collect { list ->
                val chosenBefore = hubs.firstOrNull { it.preferred }?.id
                hubs = list
                // A new chosen hub during a session: one fresh scan (the friend's tap, no
                // background work) tells whether it is in range, so the session can move to it.
                val chosen = list.firstOrNull { it.preferred }?.id
                if (chosen != null && chosen != chosenBefore && inSession()) probe.requestScan()
                pokes.trySend(Unit)
            }
        }
        lifecycleScope.launch {
            RelayStateHolder.switchRequests.collect {
                switchNow = true
                pokes.trySend(Unit)
            }
        }
        lifecycleScope.launch { app.repo.sessionStart.collect { sessionStart = it } }
        lifecycleScope.launch { app.usage.flow.collect { pokes.trySend(Unit) } }
        lifecycleScope.launch { RelayStateHolder.wakeups.collect { if (searching()) pokes.trySend(Unit) } }
        // No wake-ups of its own: Android scans anyway (screen on, our own requests); a saved
        // hub in the new results ends the search wait at once (phase 19).
        ContextCompat.registerReceiver(
            this, scanReceiver, IntentFilter(WifiManager.SCAN_RESULTS_AVAILABLE_ACTION), ContextCompat.RECEIVER_NOT_EXPORTED,
        )
        RelayStateHolder.update { it.copy(sessionShared = 0) }
    }

    private val scanReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            scans.trySend(Unit)
            if (searching() && HubPicker.wakeFor(hubs, probe.scanResults(), cooldown, now())) pokes.trySend(Unit)
            // During a session the results Android gets anyway keep the chosen-hub check current.
            if (inSession()) lastScan = HubPicker.sessionScan(lastScan, probe.scanResults())
        }
    }

    private fun searching() = main != null && RelayStateHolder.flow.value.phase == Phase.SEARCHING

    private fun inSession() = main != null && RelayStateHolder.flow.value.phase.let { it == Phase.CONNECTING || it == Phase.RELAYING }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        if (intent?.action == ACTION_STOP) {
            lifecycleScope.launch {
                app.repo.setEnabled(false)
                shutdown("user")
            }
            return START_NOT_STICKY
        }
        try {
            startInForeground()
        } catch (e: IllegalStateException) {
            // Android 12+ refuses a background FGS start (e.g. a sticky restart) unless the app is
            // exempt from battery optimisation; the toggle stays ON and the next app open resumes.
            app.log.w(TAG, "foreground start refused", e)
            stopSelf()
            return START_NOT_STICKY
        }
        if (main == null) main = lifecycleScope.launch { run() }
        return START_STICKY
    }

    override fun onDestroy() {
        runCatching { unregisterReceiver(scanReceiver) }
        main?.cancel()
        networks.stop()
        releaseLocks()
        core?.close()
        core = null
        RelayStateHolder.update { it.copy(phase = Phase.OFF, hubId = "", pausedReason = "") }
        super.onDestroy()
    }

    private suspend fun run() {
        if (!app.repo.enabled.first()) return shutdown("")
        // The collectors in onCreate may not have delivered yet: decisions on the
        // default settings would stop on a limit the owner never set.
        settings = app.repo.settings.first()
        hubs = app.repo.hubs.first()
        sessionStart = app.repo.sessionStart.first()
        app.usage.awaitLoaded()
        val c = try {
            Xscore.newCore(app.repo.logDir().absolutePath, Build.MODEL, probe.carrier())
        } catch (e: Exception) {
            app.log.w(TAG, "core", e)
            return shutdown("error: ${e.message}")
        }
        core = c
        app.log.i(TAG, "relay on")
        var attempt = 0
        var paused: String? = null
        while (lifecycleScope.isActive) {
            val reason = paused ?: StopPolicy.stopReason(settings, app.usage.roll(settings, sessionStart), device(), now())
            paused = null
            if (reason == StopPolicy.LIMIT) { // the owner turns sharing on again by hand (decisions.md)
                app.repo.setEnabled(false)
                notes.showStopped(RelayNotifications.pausedText(this, reason))
                return shutdown(reason)
            }
            if (reason != null) {
                pause(reason)
                attempt = 0
                continue
            }
            val hub = search(attempt)
            if (hub == null) {
                attempt++
                continue
            }
            attempt = 0
            when (val out = session(c, hub)) {
                is Outcome.Paused -> paused = out.reason
                Outcome.Forgotten -> app.repo.forgetHub(hub.id)
                Outcome.Cooldown -> cooldown[hub.id] = now() + HubPicker.COOLDOWN_MS
                is Outcome.CooldownFor -> cooldown[hub.id] = now() + out.ms
                Outcome.Again -> waitPoke(RETRY_MS)
            }
        }
    }

    /** One scan; returns the hub to join or waits the backoff and returns null. */
    private suspend fun search(attempt: Int): SavedHub? {
        publish { it.copy(phase = Phase.SEARCHING, hubId = "", pausedReason = "") }
        notes.show(RelayStateHolder.flow.value, "")
        val list = hubs
        if (list.isEmpty()) {
            waitPoke(null)
            return null
        }
        scans.tryReceive() // drop an older "scan done"
        val scanStarted = probe.requestScan()
        // Wi-Fi off: Android rejects the hub's Wi-Fi request at once, which looked like a
        // declined hub (5 min cooldown, phase 18). Wait for Wi-Fi instead, without a cooldown.
        if (!probe.wifiOn()) {
            publish { it.copy(wifiOff = true) }
            waitPoke(WIFI_OFF_CHECK_MS)
            return null
        }
        publish { it.copy(wifiOff = false) }
        // Read the results of this scan, not the previous one (the hub was missed for a whole
        // backoff right after it came back, phase 19); Android may throttle the request.
        if (scanStarted) withTimeoutOrNull(SCAN_RESULT_WAIT_MS) { scans.receive() }
        // In the background Android gives scan results only with background location; with
        // "while using the app" the service got an empty list and never joined (phase 18).
        // An empty scan here is "unknown": the picker joins the chosen or last used hub and
        // the Wi-Fi request finds it by itself.
        val scan = probe.scanResults()?.takeIf { it.isNotEmpty() }
        lastScan = scan
        publish { it.copy(inRange = scan?.let { s -> HubPicker.inRange(list, s) }) }
        if (forceChosen) { // "Switch now": try the chosen hub even when this scan missed it
            forceChosen = false
            list.firstOrNull { it.preferred }?.let { return it }
        }
        HubPicker.pick(list, scan, cooldown, now())?.let { return it }
        if (attempt % 5 == 0) { // why nothing is joined (field logs), not on every scan
            val cooling = cooldown.filterValues { it > now() }.keys.mapNotNull { id -> list.firstOrNull { it.id == id }?.name }
            app.log.i(TAG, "no hub to join: scan=${scan?.size ?: "unavailable"} in range=${HubPicker.inRange(list, scan ?: emptyList()).size} cooling=$cooling")
        }
        waitPoke(HubPicker.backoff(attempt))
        return null
    }

    private sealed interface Outcome {
        data class Paused(val reason: String) : Outcome
        data object Forgotten : Outcome
        data object Cooldown : Outcome
        data object Again : Outcome
        data class CooldownFor(val ms: Long) : Outcome
    }

    /** The friend's current "how can this hub use my data" choice (live from the saved hubs). */
    private fun useOf(hub: SavedHub): HubUse = hubs.firstOrNull { it.id == hub.id }?.use ?: hub.use

    /** Joins the hub's Wi-Fi and relays until the hub is lost, a stop condition or a hub verdict. */
    private suspend fun session(c: Core, hub: SavedHub): Outcome {
        val q = runCatching { QrInfo.fromJson(Xscore.parseQR(hub.qr)) }.getOrElse { return Outcome.Forgotten }
        while (stopEvents.tryReceive().isSuccess) { // drop events of an earlier session
        }
        publish { it.copy(phase = Phase.CONNECTING, hubId = hub.id) }
        app.log.i(TAG, "joining hub ${hub.name}")
        wifiHandle = 0
        wifiUnavailable = false
        val wifiAskedAt = now()
        cellValidated = false
        cellBadSince = now()
        networks.startCellular()
        try {
            networks.startWifi(q)
        } catch (e: IllegalArgumentException) { // e.g. a BSSID form MacAddress does not parse
            app.log.w(TAG, "hub Wi-Fi request", e)
            networks.stop()
            cellValidated = true
            cellBadSince = null
            return Outcome.Cooldown
        }
        val dir = app.repo.hubDir(hub.id).apply { mkdirs() }
        try {
            c.start(dir.absolutePath, hub.qr, settings.limitsJson(app.usage.roll(settings, sessionStart).used, useOf(hub)), stopCallback)
        } catch (e: Exception) {
            app.log.w(TAG, "core start", e)
            networks.stop()
            cellValidated = true // only a running session decides no_data
            cellBadSince = null
            return Outcome.Again
        }
        acquireLocks()
        var lastShared = 0L
        var hubBytes = 0L
        var limitsKey: Any = Triple(settings, app.usage.flow.value.periodStart, useOf(hub))
        var lostSince: Long? = null
        var connected = false
        var lastFlush = now()
        var n = 0
        try {
            while (true) {
                withTimeoutOrNull(TICK_MS) { stopEvents.receive() }?.let { (kind, reason) -> return verdict(kind, reason) }
                val t = now()
                val (dbm, type) = probe.cellular()
                c.setDevice(probe.batteryPercent().toLong(), dbm.toLong(), type, cellValidated, wifiRssi.toLong())
                val st = RelayStats.fromJson(c.stats())
                val delta = (st.sharedBytes - lastShared).coerceAtLeast(0)
                lastShared = st.sharedBytes
                hubBytes += delta
                app.usage.add(delta, settings, sessionStart, t)
                val usage = app.usage.roll(settings, sessionStart, t)
                val key = Triple(settings, usage.periodStart, useOf(hub))
                if (key != limitsKey) { // settings changed, new period, counter reset or hub use changed
                    limitsKey = key
                    runCatching { c.setLimits(settings.limitsJson(usage.used, useOf(hub))) }.onFailure { app.log.w(TAG, "limits", it) }
                }
                StopPolicy.stopReason(settings, usage, device(), t)?.let { reason ->
                    app.log.i(TAG, "stopping: $reason")
                    c.stopWith(reason)
                    awaitStop()
                    return Outcome.Paused(reason)
                }
                if (hubs.none { it.id == hub.id }) return stopAnd(c, Outcome.Again) // forgotten in the app
                if (switchNow) {
                    switchNow = false
                    HubPicker.switchOffer(hubs, hub.id, inSession = true)?.let { chosen ->
                        app.log.i(TAG, "leaving ${hub.name} for ${chosen.name} (switch now)")
                        cooldown.remove(chosen.id)
                        forceChosen = true
                        return stopAnd(c, Outcome.Again)
                    }
                }
                if (HubPicker.switchTo(hubs, hub.id, lastScan, cooldown, t)) { // the friend chose another hub
                    app.log.i(TAG, "leaving ${hub.name} for the chosen hub")
                    return stopAnd(c, Outcome.Again)
                }
                if (wifiUnavailable) {
                    val out = if (!probe.wifiOn()) {
                        Outcome.Again
                    } else {
                        Outcome.CooldownFor(HubPicker.unavailableCooldown(t - wifiAskedAt, HubNetworks.WIFI_TIMEOUT_MS.toLong()))
                    }
                    return stopAnd(c, out)
                }
                if (wifiHandle == 0L) {
                    val since = lostSince ?: t.also { lostSince = it }
                    if (t - since >= if (connected) WIFI_LOST_MS else HubNetworks.WIFI_TIMEOUT_MS + WIFI_LOST_MS) {
                        app.log.i(TAG, "hub Wi-Fi lost")
                        return stopAnd(c, Outcome.Again)
                    }
                } else {
                    lostSince = null
                }
                connected = connected || st.connected
                val phase = if (st.connected) Phase.RELAYING else Phase.CONNECTING
                publish { it.copy(phase = phase, stats = st, sessionShared = it.sessionShared + delta) }
                if (n++ % NOTIFY_EVERY == 0) notes.show(RelayStateHolder.flow.value, (hubs.firstOrNull { it.id == hub.id } ?: hub).shownName)
                if (t - lastFlush >= FLUSH_MS) {
                    lastFlush = t
                    flush(hub.id, if (connected) hubBytes else -1).also { hubBytes = 0 }
                }
            }
        } finally {
            withContext(NonCancellable) {
                networks.stopWifi()
                networks.stop()
                runCatching { c.setNetwork(HubNetworks.KIND_WIFI, 0) }
                cellValidated = true // only a running session decides no_data
                cellBadSince = null
                releaseLocks()
                flush(hub.id, if (connected) hubBytes else -1)
                publish { it.copy(stats = it.stats.copy(running = false, connected = false)) }
            }
        }
    }

    private suspend fun stopAnd(c: Core, out: Outcome): Outcome {
        c.stop()
        awaitStop()
        return out
    }

    private suspend fun awaitStop() {
        withTimeoutOrNull(STOP_WAIT_MS) { stopEvents.receive() }
    }

    private fun verdict(kind: String, reason: String): Outcome {
        app.log.i(TAG, "relay ended: $kind $reason")
        publish { it.copy(lastStop = if (reason.isEmpty()) kind else "$kind: $reason") }
        return when (kind) {
            Xscore.StopLimit -> Outcome.Paused(reason)
            Xscore.StopForgotten -> Outcome.Forgotten
            Xscore.StopRejected -> Outcome.Cooldown
            else -> Outcome.Again
        }
    }

    /** Paused by reason until StopPolicy allows a restart; re-checked every 60 s or on a settings/usage change. */
    private suspend fun pause(reason: String) {
        app.log.i(TAG, "paused: $reason")
        publish { it.copy(phase = Phase.PAUSED, hubId = "", pausedReason = reason) }
        notes.show(RelayStateHolder.flow.value, "")
        if (reason == StopPolicy.NO_DATA) {
            cellValidated = false
            networks.startCellular()
        }
        try {
            while (!StopPolicy.canResume(reason, settings, app.usage.roll(settings, sessionStart), device(), now())) {
                waitPoke(PAUSE_CHECK_MS)
            }
        } finally {
            networks.stop()
        }
        app.log.i(TAG, "resuming after $reason")
    }

    private suspend fun flush(hubId: String, hubBytes: Long) {
        app.usage.flush()
        if (hubBytes >= 0) app.repo.touchHub(hubId, now(), hubBytes)
    }

    private suspend fun waitPoke(ms: Long?) {
        if (ms == null) pokes.receive() else withTimeoutOrNull(ms) { pokes.receive() }
    }

    private fun device() = DeviceState(
        battery = probe.batteryPercent(), charging = probe.charging(),
        cellularValidated = cellValidated, cellularBadSince = cellBadSince,
    )

    private fun shutdown(why: String) {
        main?.cancel()
        main = null
        if (why.isNotEmpty()) app.log.i(TAG, "relay off: $why")
        RelayStateHolder.update { it.copy(phase = Phase.OFF, hubId = "", pausedReason = "", lastStop = why.ifEmpty { it.lastStop }) }
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun publish(f: (RelayUiState) -> RelayUiState) = RelayStateHolder.update(f)

    private val networkListener = object : HubNetworks.Listener {
        override fun onNetwork(kind: String, handle: Long) {
            if (kind == HubNetworks.KIND_WIFI) wifiHandle = handle
            runCatching { core?.setNetwork(kind, handle) }.onFailure { app.log.w(TAG, "setNetwork", it) }
        }

        override fun onCellularValidated(ok: Boolean) {
            cellValidated = ok
            cellBadSince = if (ok) null else cellBadSince ?: now()
            pokes.trySend(Unit)
        }

        override fun onWifiRssi(rssi: Int) {
            wifiRssi = rssi
        }

        override fun onWifiUnavailable() {
            app.log.i(TAG, "hub Wi-Fi unavailable (not found or declined)")
            wifiUnavailable = true
        }
    }

    private val stopCallback = object : Callback {
        override fun onStopped(kind: String, reason: String) {
            stopEvents.trySend(kind to reason)
        }
    }

    private fun startInForeground() {
        notes.ensureChannel()
        ServiceCompat.startForeground(
            this,
            RelayNotifications.ID,
            notes.build(RelayStateHolder.flow.value, ""),
            // specialUse exists from API 34; older releases take no type.
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0,
        )
    }

    @SuppressLint("WakelockTimeout") // held exactly while a hub session runs; released when it ends
    private fun acquireLocks() {
        if (wakeLock == null) {
            wakeLock = getSystemService(PowerManager::class.java)
                .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "CrossStream:relay").apply { acquire() }
        }
        if (wifiLock == null) {
            @Suppress("DEPRECATION") // HIGH_PERF is the only mode that also holds with the screen off
            wifiLock = getSystemService(WifiManager::class.java)
                .createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "CrossStream:relay").apply { acquire() }
        }
    }

    private fun releaseLocks() {
        wakeLock?.takeIf { it.isHeld }?.release()
        wifiLock?.takeIf { it.isHeld }?.release()
        wakeLock = null
        wifiLock = null
    }

    private fun now() = System.currentTimeMillis()

    companion object {
        private const val TAG = "RelayService"
        private const val TICK_MS = 1_000L
        private const val NOTIFY_EVERY = 5 // ticks between notification updates
        private const val FLUSH_MS = 30_000L
        private const val WIFI_LOST_MS = 15_000L
        private const val STOP_WAIT_MS = 5_000L
        /** How often a relay waiting for Wi-Fi checks whether it is back on. */
        private const val WIFI_OFF_CHECK_MS = 5_000L
        private const val RETRY_MS = 2_000L
        private const val PAUSE_CHECK_MS = 60_000L
        /** How long a search waits for the results of the scan it asked for. */
        private const val SCAN_RESULT_WAIT_MS = 5_000L
        const val ACTION_START = "com.zyndata.crossstream.relay.START"
        const val ACTION_STOP = "com.zyndata.crossstream.relay.STOP"

        fun start(context: Context) {
            context.startForegroundService(Intent(context, RelayService::class.java).setAction(ACTION_START))
        }

        fun stop(context: Context) {
            context.startService(Intent(context, RelayService::class.java).setAction(ACTION_STOP))
        }
    }
}
