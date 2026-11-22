package com.zyndata.crossstream.relay.ui

import android.Manifest
import android.content.Intent
import android.location.LocationManager
import android.widget.Toast
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.RadioButton
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.Spacer
import androidx.compose.ui.draw.scale
import androidx.compose.material3.Switch
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.zyndata.crossstream.relay.R
import com.zyndata.crossstream.relay.RelayApplication
import com.zyndata.crossstream.relay.RelayNotifications
import com.zyndata.crossstream.relay.RelayService
import com.zyndata.crossstream.relay.logic.HubPicker
import com.zyndata.crossstream.relay.logic.HubState
import com.zyndata.crossstream.relay.logic.Period
import com.zyndata.crossstream.relay.logic.RelaySettings
import com.zyndata.crossstream.relay.logic.SavedHub
import com.zyndata.crossstream.relay.logic.Usage
import com.zyndata.crossstream.relay.model.Phase
import com.zyndata.crossstream.relay.model.RelayStateHolder
import com.zyndata.crossstream.relay.model.RelayUiState
import com.zyndata.crossstream.relay.model.formatData
import com.zyndata.crossstream.relay.net.DeviceProbe
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

private sealed interface Screen {
    data object Home : Screen
    data object Scan : Screen
    data object Settings : Screen
    data class Hub(val id: String) : Screen
}

/** App navigation: Home, Add hub (QR), Hub detail, Settings. */
@Composable
fun RelayApp() {
    val context = LocalContext.current
    val app = remember { RelayApplication.of(context) }
    val scope = rememberCoroutineScope()
    var screen by remember { mutableStateOf<Screen>(Screen.Home) }
    BackHandler(enabled = screen != Screen.Home) { screen = Screen.Home }

    val hubs by app.repo.hubs.collectAsStateWithLifecycle(emptyList())
    val enabled by app.repo.enabled.collectAsStateWithLifecycle(false)
    val settings by app.repo.settings.collectAsStateWithLifecycle(RelaySettings())
    val usage by app.usage.flow.collectAsStateWithLifecycle()
    val state by RelayStateHolder.flow.collectAsStateWithLifecycle()

    // Sharing is on but the service is gone (killed, and Android refused the
    // background restart): opening the app brings it back.
    LaunchedEffect(enabled) {
        if (enabled && !RelayStateHolder.flow.value.serviceRunning) {
            runCatching { RelayService.start(context) }
        }
    }

    when (val s = screen) {
        Screen.Scan -> ScanScreen(
            onCode = { code ->
                val hub = RelayApplication.hubFromQr(code)
                if (hub != null) {
                    scope.launch { app.repo.saveHub(hub) }
                    app.log.i("RelayApp", "hub added: ${hub.name}")
                    screen = Screen.Home
                }
                hub != null
            },
            onCancel = { screen = Screen.Home },
        )
        Screen.Settings -> SettingsScreen(settings, usage, onBack = { screen = Screen.Home })
        is Screen.Hub -> {
            val hub = hubs.firstOrNull { it.id == s.id }
            if (hub == null) {
                screen = Screen.Home
            } else {
                HubDetailScreen(hub, hubState(hub, state, null), onBack = { screen = Screen.Home }, onUse = { use ->
                    scope.launch { app.repo.setHubUse(hub.id, use) }
                }, onChoose = { on ->
                    scope.launch { app.repo.setPreferredHub(if (on) hub.id else "") }
                    app.log.i("RelayApp", if (on) "hub chosen: ${hub.name}" else "hub choice: automatic")
                }, onForget = {
                    scope.launch { app.repo.forgetHub(hub.id) }
                    app.log.i("RelayApp", "hub forgotten: ${hub.name}")
                    screen = Screen.Home
                })
            }
        }
        Screen.Home -> HomeScreen(
            hubs, enabled, settings, usage, state,
            onToggle = { on ->
                scope.launch {
                    app.repo.setEnabled(on)
                    if (on) RelayService.start(context) else if (state.serviceRunning) RelayService.stop(context)
                }
            },
            onAdd = { screen = Screen.Scan },
            onHub = { screen = Screen.Hub(it) },
            onSettings = { screen = Screen.Settings },
            onChoose = { id -> scope.launch { app.repo.setPreferredHub(id) } },
        )
    }
}

/** visible: saved hubs in the scan the UI read itself; null = the UI cannot read scans. */
private fun hubState(hub: SavedHub, state: RelayUiState, visible: Set<String>?): HubState {
    val range = visible ?: state.inRange
    return when {
        state.hubId == hub.id && state.phase == Phase.RELAYING -> HubState.CONNECTED
        state.hubId == hub.id && state.phase == Phase.CONNECTING -> HubState.CONNECTING
        range == null -> HubState.UNKNOWN
        hub.id in range -> HubState.IN_RANGE
        else -> HubState.AWAY
    }
}

@Composable
private fun HomeScreen(
    hubs: List<SavedHub>,
    enabled: Boolean,
    settings: RelaySettings,
    usage: Usage,
    state: RelayUiState,
    onToggle: (Boolean) -> Unit,
    onAdd: () -> Unit,
    onHub: (String) -> Unit,
    onSettings: () -> Unit,
    onChoose: (String) -> Unit,
) {
    val context = LocalContext.current
    val permissions = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        onToggle(true) // works without them, just with less: no notification, no scan → last used hub
    }
    // With the relay off, scan results the system already has show which hubs are in range;
    // "Refresh" asks for a fresh scan (user tap only, no background work) and reads them again.
    var scanTick by remember { mutableStateOf(0) }
    var scanning by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    // The phone's cached scan results, read again every few seconds (reading starts no scan);
    // "Refresh" asks for a fresh scan while the relay is off (user tap only, no background work).
    LaunchedEffect(Unit) {
        while (true) {
            delay(RANGE_READ_MS)
            scanTick++
        }
    }
    val visible = remember(hubs, scanTick) { DeviceProbe(context).scanResults()?.let { HubPicker.inRange(hubs, it) } }
    // The screen sees a saved hub the searching service has not found yet: search now (phase 19).
    LaunchedEffect(visible, state.phase) {
        if (state.phase == Phase.SEARCHING && !visible.isNullOrEmpty()) RelayStateHolder.wakeSearch()
    }
    Column(
        modifier = Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(20.dp).fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.app_name), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
            TextButton(onClick = onSettings) { Text(stringResource(R.string.settings)) }
        }
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.Center,
            modifier = Modifier.fillMaxWidth().padding(vertical = 18.dp),
        ) {
            Text(stringResource(R.string.toggle_label), style = MaterialTheme.typography.headlineSmall)
            Spacer(Modifier.width(40.dp))
            // Material switch at 2×: the one control of the app, easy to hit and to read.
            Switch(
                checked = enabled,
                onCheckedChange = { on -> if (on) permissions.launch(relayPermissions()) else onToggle(false) },
                enabled = enabled || hubs.isNotEmpty(),
                modifier = Modifier.scale(2f),
            )
        }
        Text(
            statusText(state, enabled, hubs),
            style = MaterialTheme.typography.titleMedium,
            textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth(),
        )
        Text(
            stringResource(R.string.shared_totals, formatData(state.sessionShared), formatData(usage.allTime)),
            textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth(),
        )
        if (!settings.unlimited) {
            Text(
                stringResource(
                    R.string.limit_line, formatData(usage.used), formatData(settings.capMb * RelaySettings.BYTES_PER_MB),
                    periodText(settings.period),
                ),
                style = MaterialTheme.typography.bodySmall,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }
        UpdateBanner()
        SetupCard()
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.hubs), style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
            if (!state.serviceRunning && hubs.isNotEmpty() && visible != null) {
                TextButton(
                    enabled = !scanning,
                    onClick = {
                        scanning = true
                        if (!DeviceProbe(context).requestScan()) {
                            Toast.makeText(context, R.string.refresh_throttled, Toast.LENGTH_LONG).show()
                        }
                        scope.launch {
                            delay(SCAN_WAIT_MS)
                            scanTick++
                            scanning = false
                        }
                    },
                ) { Text(stringResource(if (scanning) R.string.refreshing_hubs else R.string.refresh_hubs)) }
            }
            OutlinedButton(onClick = onAdd) { Text(stringResource(R.string.add_hub)) }
        }
        if (hubs.isEmpty()) Text(stringResource(R.string.no_hub))
        if (hubs.isNotEmpty() && visible == null) LocationHint(scanTick)
        // Online hubs first, then unknown range, offline last. With several online the friend
        // picks the active one with a radio button (it becomes the chosen hub).
        val states = hubs.associate { it.id to hubState(it, state, visible) }
        val online = hubs.filter { states[it.id] in ONLINE }.sortedByDescending { it.lastSeen }
        val rest = hubs.filter { states[it.id] !in ONLINE }
            .sortedWith(compareBy<SavedHub> { states[it.id] == HubState.AWAY }.thenByDescending { it.lastSeen })
        val active = online.firstOrNull { it.preferred }?.id
            ?: online.firstOrNull { states[it.id] == HubState.CONNECTED || states[it.id] == HubState.CONNECTING }?.id
        online.forEach { hub ->
            HubCard(hub, states.getValue(hub.id), selected = if (online.size > 1) hub.id == active else null,
                onSelect = { onChoose(hub.id) }) { onHub(hub.id) }
        }
        rest.forEach { hub -> HubCard(hub, states.getValue(hub.id)) { onHub(hub.id) } }
        if (state.lastStop.isNotEmpty() && !enabled) {
            Text(stringResource(R.string.stats_last_stop, state.lastStop), style = MaterialTheme.typography.bodySmall)
        }
    }
}

@Composable
private fun statusText(state: RelayUiState, enabled: Boolean, hubs: List<SavedHub>): String {
    val name = hubs.firstOrNull { it.id == state.hubId }?.name.orEmpty()
    return when {
        !enabled -> stringResource(R.string.status_off)
        state.wifiOff -> stringResource(R.string.status_wifi_off)
        state.phase == Phase.RELAYING -> stringResource(R.string.notif_relaying, name)
        state.phase == Phase.CONNECTING -> stringResource(R.string.notif_connecting, name)
        state.phase == Phase.PAUSED ->
            stringResource(R.string.notif_paused, RelayNotifications.pausedText(LocalContext.current, state.pausedReason))
        else -> stringResource(R.string.notif_searching)
    }
}

@Composable
fun periodText(p: Period): String = stringResource(
    when (p) {
        Period.SESSION -> R.string.period_session
        Period.DAY -> R.string.period_day
        Period.MONTH -> R.string.period_month
    },
)

@Composable
fun hubStateText(s: HubState): String = stringResource(
    when (s) {
        HubState.CONNECTED -> R.string.hub_connected
        HubState.CONNECTING -> R.string.hub_connecting
        HubState.IN_RANGE -> R.string.hub_in_range
        HubState.AWAY -> R.string.hub_away
        HubState.UNKNOWN -> R.string.hub_range_unknown
    },
)

@Composable
private fun HubCard(hub: SavedHub, state: HubState, selected: Boolean? = null, onSelect: () -> Unit = {}, onClick: () -> Unit) {
    Card(modifier = Modifier.fillMaxWidth().clickable(onClick = onClick)) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            if (selected != null) {
                RadioButton(selected = selected, onClick = onSelect)
                Spacer(Modifier.width(8.dp))
            }
            Column(Modifier.weight(1f)) {
                Text(
                    if (hub.preferred) stringResource(R.string.hub_name_chosen, hub.name) else hub.name,
                    style = MaterialTheme.typography.titleMedium,
                )
                Text(stringResource(R.string.hub_shared, formatData(hub.sharedBytes)), style = MaterialTheme.typography.bodySmall)
            }
            if (state != HubState.UNKNOWN) {
                Text(
                    hubStateText(state),
                    color = if (state == HubState.CONNECTED) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

/** Hub states listed first (and offered as the active hub when there are several). */
private val ONLINE = setOf(HubState.CONNECTED, HubState.CONNECTING, HubState.IN_RANGE)

/** How often the home screen reads the phone's cached scan results again. */
private const val RANGE_READ_MS = 5_000L

/** Time a requested Wi-Fi scan usually needs before its results are readable. */
private const val SCAN_WAIT_MS = 4_000L

/**
 * Shown while the phone cannot tell which hubs are in range: Android gives Wi-Fi scan results
 * only to apps with location access and location on. Optional: without it the relay joins the
 * chosen or last used hub (decisions.md phase 18).
 */
@Composable
private fun LocationHint(tick: Int) {
    val context = LocalContext.current
    var asked by remember { mutableStateOf(0) }
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { asked++ }
    val permitted = remember(tick, asked) { granted(context, Manifest.permission.ACCESS_FINE_LOCATION) }
    val on = remember(tick, asked) { context.getSystemService(LocationManager::class.java)?.isLocationEnabled == true }
    if (permitted && on) return // both in place: the next scan shows the range
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(stringResource(R.string.range_hint), style = MaterialTheme.typography.bodyMedium)
            if (!permitted) {
                OutlinedButton(onClick = {
                    ask.launch(arrayOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_COARSE_LOCATION))
                }) { Text(stringResource(R.string.range_allow)) }
            } else {
                OutlinedButton(onClick = {
                    context.startActivity(Intent(android.provider.Settings.ACTION_LOCATION_SOURCE_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                }) { Text(stringResource(R.string.range_location_on)) }
            }
        }
    }
}
