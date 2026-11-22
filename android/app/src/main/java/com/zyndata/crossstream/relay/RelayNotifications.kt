package com.zyndata.crossstream.relay

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import com.zyndata.crossstream.relay.model.Phase
import com.zyndata.crossstream.relay.model.RelayUiState
import com.zyndata.crossstream.relay.model.formatData

/** The relay's foreground notification: state, shared data, Stop action. */
class RelayNotifications(private val context: Context) {
    private val nm = context.getSystemService(NotificationManager::class.java)

    fun ensureChannel() {
        if (nm.getNotificationChannel(CHANNEL_ID) == null) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, context.getString(R.string.channel_name), NotificationManager.IMPORTANCE_LOW),
            )
        }
    }

    fun show(state: RelayUiState, hubName: String) = nm.notify(ID, build(state, hubName))

    /** A dismissible notice that sharing was switched off (the foreground notification goes away). */
    fun showStopped(reason: String) {
        val open = PendingIntent.getActivity(
            context, 0, Intent(context, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        nm.notify(
            STOPPED_ID,
            NotificationCompat.Builder(context, CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_relay)
                .setContentTitle(context.getString(R.string.notif_stopped, reason))
                .setContentText(context.getString(R.string.notif_stopped_text))
                .setContentIntent(open)
                .setAutoCancel(true)
                .build(),
        )
    }

    fun build(state: RelayUiState, hubName: String): Notification {
        val open = PendingIntent.getActivity(
            context, 0, Intent(context, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            context, 1, Intent(context, RelayService::class.java).setAction(RelayService.ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val title = when (state.phase) {
            Phase.RELAYING -> context.getString(R.string.notif_relaying, hubName)
            Phase.CONNECTING -> context.getString(R.string.notif_connecting, hubName)
            Phase.PAUSED -> context.getString(R.string.notif_paused, pausedText(context, state.pausedReason))
            Phase.SEARCHING, Phase.OFF -> context.getString(R.string.notif_searching)
        }
        return NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_relay)
            .setContentTitle(title)
            .setContentText(context.getString(R.string.notif_shared, formatData(state.sessionShared)))
            .setContentIntent(open)
            .addAction(0, context.getString(R.string.stop), stop)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .build()
    }

    companion object {
        const val ID = 1
        private const val STOPPED_ID = 2
        private const val CHANNEL_ID = "relay"

        /** Human text for a pause / stop reason (control.Leaving values). */
        fun pausedText(context: Context, reason: String): String = context.getString(
            when (reason) {
                "limit" -> R.string.reason_limit
                "battery" -> R.string.reason_battery
                "no_data", "no_cellular" -> R.string.reason_no_data
                else -> R.string.reason_other
            },
        )
    }
}
