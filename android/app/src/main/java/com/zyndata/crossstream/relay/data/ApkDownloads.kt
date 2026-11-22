package com.zyndata.crossstream.relay.data

import android.app.DownloadManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Environment
import android.provider.Settings
import androidx.core.content.FileProvider
import androidx.core.net.toUri
import com.zyndata.crossstream.relay.logic.DownloadWatch
import com.zyndata.crossstream.relay.logic.Updates
import java.io.File
import kotlinx.coroutines.delay

/**
 * Fetches a release APK and hands it to the package installer.
 *
 * `DownloadManager` does the transfer (no storage permission, no download code of our own);
 * the file lands in this app's external files directory. Installing needs
 * `REQUEST_INSTALL_PACKAGES` and the user's "install unknown apps" permission, granted in a
 * Settings screen. Android itself refuses an APK signed with another key.
 */
class ApkDownloads(context: Context) {
    private val appContext = context.applicationContext
    private val manager = appContext.getSystemService(DownloadManager::class.java)

    sealed interface Progress {
        data class Done(val apk: Uri) : Progress
        data object Failed : Progress
    }

    /** The APK of [release] when it is already here and whole (no second download). */
    fun ready(release: Updates.Release): Uri? {
        val file = destination(Updates.apkName(release.version)) ?: return null
        if (!file.isFile || !Updates.isComplete(file.length(), release.apkSize)) return null
        return shareable(file)
    }

    /**
     * Starts the transfer and returns its id, or null when there is no DownloadManager. A
     * transfer of the same APK still running (the app was closed and reopened meanwhile) is
     * followed instead of starting a second one; this app's other transfers are dropped.
     */
    fun enqueue(release: Updates.Release): Long? {
        val manager = manager ?: return null
        val active = DownloadManager.STATUS_PENDING or DownloadManager.STATUS_RUNNING or DownloadManager.STATUS_PAUSED
        var running: Long? = null
        runCatching {
            manager.query(DownloadManager.Query().setFilterByStatus(active))?.use { c ->
                while (c.moveToNext()) {
                    val id = c.getLong(c.getColumnIndexOrThrow(DownloadManager.COLUMN_ID))
                    val uri = c.getString(c.getColumnIndexOrThrow(DownloadManager.COLUMN_URI))
                    if (running == null && uri == release.apkUrl) running = id else manager.remove(id)
                }
            }
        }
        running?.let { return it }
        val name = Updates.apkName(release.version)
        destination(name)?.delete()
        val request = DownloadManager.Request(release.apkUrl.toUri())
            .setTitle(name)
            .setMimeType(APK_TYPE)
            .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
            .setDestinationInExternalFilesDir(appContext, Environment.DIRECTORY_DOWNLOADS, name)
        return runCatching { manager.enqueue(request) }.getOrNull()
    }

    /**
     * Polls [id] until it finishes, fails or stalls ([DownloadWatch]; only while the app's
     * coroutine scope lives). A stalled transfer is removed.
     */
    suspend fun awaitFinish(id: Long): Progress {
        val manager = manager ?: return Progress.Failed
        val watch = DownloadWatch(STALL_POLLS)
        while (true) {
            var local: String? = null
            val snapshot = runCatching {
                manager.query(DownloadManager.Query().setFilterById(id))?.use { c ->
                    if (!c.moveToFirst()) return@use null
                    local = c.getString(c.getColumnIndexOrThrow(DownloadManager.COLUMN_LOCAL_URI))
                    val bytes = c.getLong(c.getColumnIndexOrThrow(DownloadManager.COLUMN_BYTES_DOWNLOADED_SO_FAR))
                    DownloadWatch.Snapshot(status(c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS))), bytes)
                }
            }.getOrNull()
            when (watch.next(snapshot)) {
                DownloadWatch.Verdict.Done -> {
                    val file = local?.toUri()?.path?.let(::File) ?: return Progress.Failed
                    return shareable(file)?.let(Progress::Done) ?: Progress.Failed
                }
                DownloadWatch.Verdict.Failed -> return Progress.Failed
                DownloadWatch.Verdict.Stalled -> {
                    runCatching { manager.remove(id) }
                    return Progress.Failed
                }
                DownloadWatch.Verdict.Wait -> delay(POLL_MS)
            }
        }
    }

    private fun status(s: Int): DownloadWatch.Status = when (s) {
        DownloadManager.STATUS_SUCCESSFUL -> DownloadWatch.Status.SUCCESSFUL
        DownloadManager.STATUS_FAILED -> DownloadWatch.Status.FAILED
        DownloadManager.STATUS_PAUSED -> DownloadWatch.Status.PAUSED
        DownloadManager.STATUS_RUNNING -> DownloadWatch.Status.RUNNING
        else -> DownloadWatch.Status.WAITING
    }

    /** A content URI the installer may read; never a file path. */
    private fun shareable(file: File): Uri? = runCatching {
        FileProvider.getUriForFile(appContext, "${appContext.packageName}.logs", file)
    }.getOrNull()

    private fun destination(name: String): File? =
        appContext.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)?.let { File(it, name) }

    /** Deletes APKs of [installed] or older (the update that started this version). */
    fun removeStale(installed: String): Int {
        val dir = appContext.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: return 0
        return dir.listFiles().orEmpty().filter { it.isFile && Updates.isStaleApk(it.name, installed) }.count { it.delete() }
    }

    /** Whether Android lets this app install an APK. */
    fun canInstall(): Boolean = appContext.packageManager.canRequestPackageInstalls()

    /** Where the user allows it when [canInstall] is false. */
    fun unknownSourcesSettings(): Intent =
        Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, "package:${appContext.packageName}".toUri())
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)

    /** Hands [apk] to the package installer; the user confirms there. */
    fun installIntent(apk: Uri): Intent = Intent(Intent.ACTION_VIEW).apply {
        setDataAndType(apk, APK_TYPE)
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
    }

    private companion object {
        const val APK_TYPE = "application/vnd.android.package-archive"
        const val POLL_MS = 500L
        const val STALL_POLLS = 4 * 60 * 2 // 4 min awake without a byte
    }
}
