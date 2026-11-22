package com.zyndata.crossstream.relay.ui

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.core.net.toUri
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.zyndata.crossstream.relay.R

/** Runtime permissions the relay asks for when the toggle is switched on. */
fun relayPermissions(): Array<String> = buildList {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
        add(Manifest.permission.POST_NOTIFICATIONS)
        add(Manifest.permission.NEARBY_WIFI_DEVICES) // scan results to find saved hubs
    } else {
        add(Manifest.permission.ACCESS_FINE_LOCATION) // scan results below Android 13
    }
    // Android 17 (API 37) blocks traffic to LAN addresses (the hub) without it.
    if (Build.VERSION.SDK_INT >= 37) add(Manifest.permission.ACCESS_LOCAL_NETWORK)
}.toTypedArray()

fun granted(context: Context, permission: String) =
    ContextCompat.checkSelfPermission(context, permission) == PackageManager.PERMISSION_GRANTED

fun ignoringBatteryOptimizations(context: Context): Boolean =
    context.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(context.packageName)

/** Asks to exempt the app from battery optimisation (sideloaded app, not subject to Play policy). */
@SuppressLint("BatteryLife")
fun requestIgnoreBatteryOptimizations(context: Context) {
    runCatching {
        context.startActivity(
            Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, "package:${context.packageName}".toUri()),
        )
    }
}

fun openAppSettings(context: Context) {
    runCatching {
        context.startActivity(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, "package:${context.packageName}".toUri()),
        )
    }
}

/** Vendor-specific hint for keeping the app alive in the background, or null. */
fun vendorHint(): Int? {
    val m = Build.MANUFACTURER.lowercase()
    return when {
        m.contains("xiaomi") || m.contains("redmi") || m.contains("poco") -> R.string.hint_xiaomi
        m.contains("samsung") -> R.string.hint_samsung
        m.contains("huawei") || m.contains("honor") -> R.string.hint_huawei
        else -> null
    }
}

/**
 * Setup steps that are still open: notifications, Wi-Fi scan permission,
 * battery optimisation. Hidden when everything is done.
 */
@Composable
fun SetupCard() {
    val context = LocalContext.current
    var refresh by remember { mutableIntStateOf(0) }
    LifecycleResumeEffect(Unit) {
        refresh++
        onPauseOrDispose { }
    }
    val launcher = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { refresh++ }
    @Suppress("UNUSED_EXPRESSION") refresh // recomposes after returning from a settings screen
    val missingPerms = relayPermissions().filterNot { granted(context, it) }
    val batteryOpt = !ignoringBatteryOptimizations(context)
    if (missingPerms.isEmpty() && !batteryOpt) return
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(stringResource(R.string.setup_title), style = MaterialTheme.typography.titleMedium)
            if (missingPerms.isNotEmpty()) {
                Text(stringResource(R.string.setup_permissions))
                OutlinedButton(onClick = { launcher.launch(missingPerms.toTypedArray()) }) {
                    Text(stringResource(R.string.setup_grant))
                }
            }
            if (batteryOpt) {
                Text(stringResource(R.string.setup_battery))
                OutlinedButton(onClick = { requestIgnoreBatteryOptimizations(context) }) {
                    Text(stringResource(R.string.setup_battery_button))
                }
            }
            vendorHint()?.let { Text(stringResource(it), style = MaterialTheme.typography.bodySmall) }
        }
    }
}
