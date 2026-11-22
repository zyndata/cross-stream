package com.zyndata.crossstream.relay.ui

import android.content.Context
import android.content.Intent
import android.widget.Toast
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.zyndata.crossstream.relay.R
import com.zyndata.crossstream.relay.RelayApplication
import com.zyndata.crossstream.relay.data.AppUpdates
import com.zyndata.crossstream.relay.logic.Updates
import kotlinx.coroutines.launch

/** Home: "Version X is available" with Download / Install, What's new and Hide. */
@Composable
fun UpdateBanner() {
    val app = RelayApplication.of(LocalContext.current)
    val release by app.updates.banner.collectAsStateWithLifecycle()
    val r = release ?: return
    val scope = rememberCoroutineScope()
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(stringResource(R.string.update_available, r.tag), style = MaterialTheme.typography.titleMedium)
            UpdateActions(r, onHide = { scope.launch { app.updates.dismiss() } })
        }
    }
}

/** Settings: installed version, check at start on/off, Check now, the release found. */
@Composable
fun UpdatesSection() {
    val context = LocalContext.current
    val app = RelayApplication.of(context)
    val scope = rememberCoroutineScope()
    val auto by app.repo.updateAuto.collectAsStateWithLifecycle(true)
    val release by app.updates.latest.collectAsStateWithLifecycle()
    var checking by remember { mutableStateOf(false) }
    var outcome by remember { mutableStateOf<AppUpdates.Outcome?>(null) }

    Text(stringResource(R.string.update_version, app.updates.installed.ifEmpty { "?" }))
    if (!app.updates.isRelease) {
        Text(stringResource(R.string.update_dev_build), style = MaterialTheme.typography.bodySmall)
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(stringResource(R.string.update_auto), modifier = Modifier.weight(1f))
        Switch(checked = auto, onCheckedChange = { on -> scope.launch { app.repo.setUpdateAuto(on) } })
    }
    Text(stringResource(R.string.update_hint), style = MaterialTheme.typography.bodySmall)
    OutlinedButton(enabled = !checking, onClick = {
        checking = true
        scope.launch {
            outcome = app.updates.check()
            checking = false
        }
    }) { Text(stringResource(if (checking) R.string.update_checking else R.string.update_check_now)) }
    when (val o = outcome) {
        is AppUpdates.Outcome.UpToDate -> Text(
            stringResource(if (app.updates.isRelease) R.string.update_up_to_date else R.string.update_latest, o.latest ?: "?"),
        )
        AppUpdates.Outcome.Unreachable -> Text(stringResource(R.string.update_unreachable))
        else -> {}
    }
    release?.let { r ->
        Text(stringResource(R.string.update_available, r.tag), style = MaterialTheme.typography.titleSmall)
        UpdateActions(r, onHide = null)
    }
}

@Composable
private fun UpdateActions(r: Updates.Release, onHide: (() -> Unit)?) {
    val context = LocalContext.current
    val app = RelayApplication.of(context)
    val state by app.updates.downloadState.collectAsStateWithLifecycle()
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
        when (val s = state) {
            AppUpdates.Download.Running -> Text(stringResource(R.string.update_downloading))
            is AppUpdates.Download.Ready -> Button(onClick = { install(context, app, s) }) {
                Text(stringResource(R.string.update_install))
            }
            else -> Button(onClick = { app.updates.startDownload() }) {
                Text(stringResource(R.string.update_download, ((r.apkSize + 500_000) / 1_000_000).coerceAtLeast(1)))
            }
        }
        TextButton(onClick = { openPage(context, r.page) }) { Text(stringResource(R.string.update_whats_new)) }
        if (onHide != null) TextButton(onClick = onHide) { Text(stringResource(R.string.update_hide)) }
    }
    if (state == AppUpdates.Download.Failed) {
        Text(stringResource(R.string.update_failed), color = MaterialTheme.colorScheme.error)
    }
}

private fun install(context: Context, app: RelayApplication, ready: AppUpdates.Download.Ready) {
    val d = app.downloads
    if (!d.canInstall()) {
        Toast.makeText(context, R.string.update_allow_install, Toast.LENGTH_LONG).show()
        runCatching { context.startActivity(d.unknownSourcesSettings()) }
        return
    }
    runCatching { context.startActivity(d.installIntent(ready.apk)) }
        .onFailure { Toast.makeText(context, R.string.update_failed, Toast.LENGTH_LONG).show() }
}

private fun openPage(context: Context, url: String) {
    runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, url.toUri()).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
}
