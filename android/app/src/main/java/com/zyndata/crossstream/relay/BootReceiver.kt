package com.zyndata.crossstream.relay

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

/**
 * Starts the relay after a reboot or an app update if the toggle was ON.
 * A specialUse foreground service may start from BOOT_COMPLETED on Android 15+
 * (docs/decisions.md, phase 08).
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val app = RelayApplication.of(context)
        val pending = goAsync()
        app.scope.launch {
            try {
                if (app.repo.enabled.first()) {
                    app.log.i(TAG, "toggle is on after ${intent.action}: starting")
                    RelayService.start(context)
                }
            } catch (e: Exception) {
                app.log.w(TAG, "start after ${intent.action}", e)
            } finally {
                pending.finish()
            }
        }
    }

    private companion object {
        const val TAG = "BootReceiver"
    }
}
