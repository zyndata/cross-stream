package com.zyndata.crossstream.relay.ui

import android.text.format.DateUtils
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.Alignment
import androidx.compose.material3.OutlinedIconButton
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.Row
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.zyndata.crossstream.relay.R
import com.zyndata.crossstream.relay.logic.HubState
import com.zyndata.crossstream.relay.logic.HubUse
import com.zyndata.crossstream.relay.logic.SavedHub
import com.zyndata.crossstream.relay.model.formatData

/** Hub detail: name, Wi-Fi, last seen, data shared, Forget (deletes the hub and its relay key). */
@Composable
fun HubDetailScreen(
    hub: SavedHub,
    state: HubState,
    onBack: () -> Unit,
    onUse: (HubUse) -> Unit,
    onRename: (String) -> Unit,
    onChoose: (Boolean) -> Unit,
    onForget: () -> Unit,
) {
    var confirm by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }
    var useInfo by remember { mutableStateOf(false) }
    Column(
        modifier = Modifier.safeDrawingPadding().padding(20.dp).fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        TextButton(onClick = onBack) { Text(stringResource(R.string.back)) }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(hub.shownName, style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
            TextButton(onClick = { renaming = true }) { Text(stringResource(R.string.hub_rename)) }
        }
        if (hub.label.isNotBlank()) Text(stringResource(R.string.hub_own_name, hub.name), style = MaterialTheme.typography.bodySmall)
        Text(hubStateText(state))
        Text(stringResource(R.string.hub_wifi, hub.ssid))
        val seen = if (hub.lastSeen == 0L) {
            stringResource(R.string.never)
        } else {
            DateUtils.getRelativeTimeSpanString(hub.lastSeen).toString()
        }
        Text(stringResource(R.string.hub_last_seen, seen))
        Text(stringResource(R.string.hub_shared, formatData(hub.sharedBytes)))
        if (hub.preferred) {
            Text(stringResource(R.string.hub_chosen_text), style = MaterialTheme.typography.bodyMedium)
            OutlinedButton(onClick = { onChoose(false) }) { Text(stringResource(R.string.hub_choose_auto)) }
        } else {
            Button(onClick = { onChoose(true) }) { Text(stringResource(R.string.hub_choose)) }
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.hub_use), style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
            OutlinedIconButton(onClick = { useInfo = true }, modifier = Modifier.size(32.dp)) {
                Text("i", fontWeight = FontWeight.Bold)
            }
        }
        Choice(stringResource(R.string.hub_use_normal), hub.use == HubUse.NORMAL) { onUse(HubUse.NORMAL) }
        Choice(stringResource(R.string.hub_use_needed), hub.use == HubUse.WHEN_NEEDED) { onUse(HubUse.WHEN_NEEDED) }
        Choice(stringResource(R.string.hub_use_backup), hub.use == HubUse.BACKUP_ONLY) { onUse(HubUse.BACKUP_ONLY) }
        Text(stringResource(R.string.hub_use_hint), style = MaterialTheme.typography.bodySmall)
        Button(
            onClick = { confirm = true },
            colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
        ) { Text(stringResource(R.string.forget)) }
    }
    if (useInfo) {
        AlertDialog(
            onDismissRequest = { useInfo = false },
            title = { Text(stringResource(R.string.hub_use)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text(stringResource(R.string.hub_use_normal_info))
                    Text(stringResource(R.string.hub_use_needed_info))
                    Text(stringResource(R.string.hub_use_backup_info))
                }
            },
            confirmButton = { TextButton(onClick = { useInfo = false }) { Text(stringResource(R.string.ok)) } },
        )
    }
    if (renaming) {
        var text by remember { mutableStateOf(hub.shownName) }
        AlertDialog(
            onDismissRequest = { renaming = false },
            title = { Text(stringResource(R.string.hub_rename)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(
                        value = text,
                        onValueChange = { text = it.take(SavedHub.LABEL_MAX) },
                        singleLine = true,
                        label = { Text(stringResource(R.string.hub_rename_label)) },
                    )
                    Text(stringResource(R.string.hub_rename_hint, hub.name), style = MaterialTheme.typography.bodySmall)
                }
            },
            confirmButton = {
                TextButton(onClick = {
                    // The hub's own name typed back is no label.
                    onRename(if (SavedHub.cleanLabel(text) == hub.name) "" else text)
                    renaming = false
                }) { Text(stringResource(R.string.save)) }
            },
            dismissButton = { TextButton(onClick = { renaming = false }) { Text(stringResource(R.string.cancel)) } },
        )
    }
    if (confirm) {
        AlertDialog(
            onDismissRequest = { confirm = false },
            title = { Text(stringResource(R.string.forget_title, hub.shownName)) },
            text = { Text(stringResource(R.string.forget_text)) },
            confirmButton = { TextButton(onClick = onForget) { Text(stringResource(R.string.forget)) } },
            dismissButton = { TextButton(onClick = { confirm = false }) { Text(stringResource(R.string.cancel)) } },
        )
    }
}
