package com.zyndata.crossstream.relay

import android.app.Application
import android.content.Context
import androidx.datastore.preferences.preferencesDataStore
import com.zyndata.crossstream.core.xscore.Xscore
import com.zyndata.crossstream.relay.data.ApkDownloads
import com.zyndata.crossstream.relay.data.AppLog
import com.zyndata.crossstream.relay.data.AppUpdates
import com.zyndata.crossstream.relay.data.Repository
import com.zyndata.crossstream.relay.data.UsageMeter
import com.zyndata.crossstream.relay.logic.SavedHub
import com.zyndata.crossstream.relay.model.QrInfo
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

private val Context.relayStore by preferencesDataStore(name = "relay")

/** App-scoped objects shared by the UI, the relay service and the boot receiver. */
class RelayApplication : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    lateinit var repo: Repository
        private set
    lateinit var usage: UsageMeter
        private set
    lateinit var log: AppLog
        private set
    lateinit var downloads: ApkDownloads
        private set
    lateinit var updates: AppUpdates
        private set

    override fun onCreate() {
        super.onCreate()
        repo = Repository(this, relayStore)
        log = AppLog(repo.logDir())
        usage = UsageMeter(repo)
        downloads = ApkDownloads(this)
        val installed = runCatching { packageManager.getPackageInfo(packageName, 0).versionName }.getOrNull().orEmpty()
        updates = AppUpdates(repo, downloads, scope, installed)
        scope.launch {
            // The APK this version was installed from is no longer needed.
            runCatching { downloads.removeStale(installed) }
            runCatching { repo.migrateLegacy(::hubFromQr) }.onFailure { log.w(TAG, "legacy hub migration", it) }
            usage.load()
        }
    }

    companion object {
        private const val TAG = "RelayApp"

        fun of(context: Context): RelayApplication = context.applicationContext as RelayApplication

        /** A saved hub from an XSR1 code, or null if the code is not valid. */
        fun hubFromQr(qr: String): SavedHub? = runCatching {
            val q = QrInfo.fromJson(Xscore.parseQR(qr))
            SavedHub(id = SavedHub.idOf(q.hpk), name = q.hub, ssid = q.ssid, bssid = q.bssid, qr = qr)
        }.getOrNull()
    }
}
