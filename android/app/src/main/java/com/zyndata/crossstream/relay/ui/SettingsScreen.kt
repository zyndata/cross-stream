package com.zyndata.crossstream.relay.ui

import android.app.LocaleManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.LocaleList
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Slider
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import com.zyndata.crossstream.relay.R
import com.zyndata.crossstream.relay.RelayApplication
import com.zyndata.crossstream.relay.logic.OverLimit
import com.zyndata.crossstream.relay.logic.Period
import com.zyndata.crossstream.relay.logic.RelaySettings
import com.zyndata.crossstream.relay.logic.ThemeMode
import com.zyndata.crossstream.relay.logic.Usage
import com.zyndata.crossstream.relay.model.formatData
import kotlin.math.roundToInt
import kotlinx.coroutines.launch

/** Settings: data limit, battery threshold, language, background operation, logs. */
@Composable
fun SettingsScreen(s: RelaySettings, usage: Usage, onBack: () -> Unit) {
    val context = LocalContext.current
    val app = remember { RelayApplication.of(context) }
    val scope = rememberCoroutineScope()
    fun update(f: (RelaySettings) -> RelaySettings) {
        scope.launch { app.repo.updateSettings(f) }
    }
    Column(
        modifier = Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(20.dp).fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        TextButton(onClick = onBack) { Text(stringResource(R.string.back)) }
        Text(stringResource(R.string.settings), style = MaterialTheme.typography.headlineSmall)

        Section(R.string.data_limit)
        LimitPresets(s, ::update)
        if (!s.unlimited) {
            Text(stringResource(R.string.period))
            Period.entries.forEach { p -> Choice(periodText(p), s.period == p) { update { it.copy(period = p) } } }
            if (s.period == Period.MONTH) {
                NumberField(R.string.reset_day, s.resetDay.toLong()) { v -> if (v in 1..28) update { it.copy(resetDay = v.toInt()) } }
                val daily = RelaySettings.suggestedDailyMb(s.capMb)
                Text(stringResource(R.string.daily_hint, formatData(daily * RelaySettings.BYTES_PER_MB)), style = MaterialTheme.typography.bodySmall)
                OutlinedButton(onClick = { update { it.copy(capMb = daily, period = Period.DAY) } }) {
                    Text(stringResource(R.string.daily_use, formatData(daily * RelaySettings.BYTES_PER_MB)))
                }
            }
            Text(stringResource(R.string.over_limit))
            Choice(stringResource(R.string.over_stop), s.overLimit == OverLimit.STOP) { update { it.copy(overLimit = OverLimit.STOP) } }
            Choice(stringResource(R.string.over_throttle, s.throttleKbps), s.overLimit == OverLimit.THROTTLE) {
                update { it.copy(overLimit = OverLimit.THROTTLE) }
            }
        }
        Text(stringResource(R.string.used_this_period, formatData(usage.used)))
        OutlinedButton(onClick = { scope.launch { app.usage.reset() } }) { Text(stringResource(R.string.reset_counter)) }

        Section(R.string.battery)
        var battery by remember(s.batteryMin) { mutableIntStateOf(s.batteryMin) }
        Text(if (battery == 0) stringResource(R.string.battery_off) else stringResource(R.string.battery_min, battery))
        Slider(
            value = battery.toFloat(),
            onValueChange = { v -> battery = (v / 5).roundToInt() * 5 },
            onValueChangeFinished = { update { it.copy(batteryMin = battery) } },
            valueRange = 0f..90f,
            steps = 17,
        )
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.battery_charging), modifier = Modifier.weight(1f))
            Switch(checked = s.batteryStopWhenCharging, onCheckedChange = { c -> update { it.copy(batteryStopWhenCharging = c) } })
        }

        Section(R.string.theme)
        val theme by app.repo.theme.collectAsState(initial = ThemeMode.SYSTEM)
        Choice(stringResource(R.string.theme_system), theme == ThemeMode.SYSTEM) { scope.launch { app.repo.setTheme(ThemeMode.SYSTEM) } }
        Choice(stringResource(R.string.theme_light), theme == ThemeMode.LIGHT) { scope.launch { app.repo.setTheme(ThemeMode.LIGHT) } }
        Choice(stringResource(R.string.theme_dark), theme == ThemeMode.DARK) { scope.launch { app.repo.setTheme(ThemeMode.DARK) } }

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            Section(R.string.language)
            LanguagePicker(context)
        }

        Section(R.string.background)
        Text(
            stringResource(if (ignoringBatteryOptimizations(context)) R.string.battery_opt_off else R.string.battery_opt_on),
        )
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { requestIgnoreBatteryOptimizations(context) }) { Text(stringResource(R.string.setup_battery_button)) }
            OutlinedButton(onClick = { openAppSettings(context) }) { Text(stringResource(R.string.app_settings)) }
        }
        vendorHint()?.let { Text(stringResource(it), style = MaterialTheme.typography.bodySmall) }
        Text(stringResource(R.string.hint_overlay), style = MaterialTheme.typography.bodySmall)

        Section(R.string.updates)
        UpdatesSection()

        Section(R.string.logs)
        Button(onClick = { shareLogs(context, app) }) { Text(stringResource(R.string.share_logs)) }
    }
}

@Composable
private fun Section(title: Int) {
    HorizontalDivider(Modifier.padding(top = 8.dp))
    Text(stringResource(title), style = MaterialTheme.typography.titleMedium)
}

@Composable
internal fun Choice(label: String, selected: Boolean, onClick: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().selectable(selected = selected, onClick = onClick, role = Role.RadioButton),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        RadioButton(selected = selected, onClick = null)
        Text(label, modifier = Modifier.padding(start = 12.dp, top = 12.dp, bottom = 12.dp))
    }
}

@Composable
private fun LimitPresets(s: RelaySettings, update: ((RelaySettings) -> RelaySettings) -> Unit) {
    var custom by remember { mutableStateOf(!s.unlimited && s.capMb !in RelaySettings.PRESETS_MB) }
    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        RelaySettings.PRESETS_MB.forEach { mb ->
            Pick(formatData(mb * RelaySettings.BYTES_PER_MB), !custom && s.capMb == mb) {
                custom = false
                update { it.copy(capMb = mb) }
            }
        }
        Pick(stringResource(R.string.limit_custom), custom) { custom = true }
        Pick(stringResource(R.string.limit_unlimited), !custom && s.unlimited) {
            custom = false
            update { it.copy(capMb = 0) }
        }
    }
    if (custom) {
        NumberField(R.string.limit_custom_mb, s.capMb.coerceAtLeast(0)) { v -> if (v > 0) update { it.copy(capMb = v) } }
    }
}

@Composable
private fun Pick(label: String, selected: Boolean, onClick: () -> Unit) {
    if (selected) Button(onClick = onClick) { Text(label) } else OutlinedButton(onClick = onClick) { Text(label) }
}

@Composable
private fun NumberField(label: Int, value: Long, onValue: (Long) -> Unit) {
    var text by remember(value) { mutableStateOf(if (value > 0) value.toString() else "") }
    val focus = LocalFocusManager.current
    // Saved when the field is left or confirmed, not per keystroke: "2" on the
    // way to "2000" must never become the live limit.
    val commit = { text.toLongOrNull()?.let { if (it != value) onValue(it) } }
    OutlinedTextField(
        value = text,
        onValueChange = { t -> text = t.filter(Char::isDigit).take(7) },
        modifier = Modifier.onFocusChanged { if (!it.isFocused) commit() },
        label = { Text(stringResource(label)) },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number, imeAction = ImeAction.Done),
        keyboardActions = KeyboardActions(onDone = { commit(); focus.clearFocus() }),
    )
}

@Composable
private fun LanguagePicker(context: Context) {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
    val lm = context.getSystemService(LocaleManager::class.java)
    var current by remember { mutableStateOf(lm.applicationLocales.toLanguageTags()) }
    listOf("" to R.string.lang_system, "en" to R.string.lang_en, "pl" to R.string.lang_pl).forEach { (tag, label) ->
        Choice(stringResource(label), current == tag) {
            current = tag
            lm.applicationLocales = LocaleList.forLanguageTags(tag)
        }
    }
}

private fun shareLogs(context: Context, app: RelayApplication) {
    val uris = app.log.files().map { FileProvider.getUriForFile(context, "${context.packageName}.logs", it) }
    if (uris.isEmpty()) return
    val send = Intent(Intent.ACTION_SEND_MULTIPLE)
        .setType("text/plain")
        .putParcelableArrayListExtra(Intent.EXTRA_STREAM, ArrayList(uris))
        .putExtra(Intent.EXTRA_SUBJECT, "CrossStream Relay logs")
        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    context.startActivity(Intent.createChooser(send, null))
}
