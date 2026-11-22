package com.zyndata.crossstream.relay.data

import android.net.Uri
import com.zyndata.crossstream.relay.logic.Updates
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/**
 * "Version X is available — Download" (docs/decisions.md, phase 18 "update notifications").
 *
 * Checks happen only while the user looks at the app: when the app's screen opens (unless
 * turned off in Settings) and on "Check now". No worker, no alarm: nothing wakes the phone
 * for this. The APK downloads only on a tap.
 */
class AppUpdates(
    private val repo: Store,
    private val downloads: ApkDownloads?,
    private val scope: CoroutineScope,
    val installed: String,
    private val source: suspend () -> String? = ::fetchLatest,
) {
    /** What is remembered between checks; [Repository] on the phone, memory in tests. */
    interface Store {
        val updateAuto: Flow<Boolean>
        suspend fun updateDismissed(): String?
        suspend fun dismissUpdate(tag: String)
    }

    /** What a check found. */
    sealed interface Outcome {
        data class Newer(val release: Updates.Release) : Outcome
        data class UpToDate(val latest: String?) : Outcome
        /**
         * No network, GitHub did not answer (also: no release published yet), or something else
         * answered in its place (a Wi-Fi login page).
         */
        data object Unreachable : Outcome
    }

    /** Where the APK download got to. */
    sealed interface Download {
        data object Idle : Download
        data object Running : Download
        data class Ready(val apk: Uri) : Download
        data object Failed : Download
    }

    /** Whether this build takes updates from releases (a local `0.0.0-dev` build does not). */
    val isRelease: Boolean = Updates.version(installed) != null

    private val found = MutableStateFlow<Updates.Release?>(null)
    private val shown = MutableStateFlow<Updates.Release?>(null)
    private val download = MutableStateFlow<Download>(Download.Idle)
    private val lock = Mutex()

    /** The newer release found by the last check (Settings), dismissed or not. */
    val latest: StateFlow<Updates.Release?> = found.asStateFlow()

    /** The release the Home banner offers: newer and not dismissed. */
    val banner: StateFlow<Updates.Release?> = shown.asStateFlow()

    val downloadState: StateFlow<Download> = download.asStateFlow()

    /** The app's screen opened: check if the user has not turned it off. */
    suspend fun checkAtStart() {
        if (isRelease && repo.updateAuto.first()) check()
    }

    /** "Check now", or the check at start. */
    suspend fun check(): Outcome = lock.withLock {
        val document = source() ?: return@withLock Outcome.Unreachable
        // A login page of the Wi-Fi in GitHub's place is no answer: keep what was found.
        val latest = Updates.latestTag(document) ?: return@withLock Outcome.Unreachable
        val offer = Updates.offer(document, installed)
        found.value = offer
        shown.value = offer?.takeUnless { it.tag == repo.updateDismissed() }
        if (offer == null) Outcome.UpToDate(latest) else Outcome.Newer(offer)
    }

    /** "Hide" on the banner: this version is not shown again on Home (a newer one is). */
    suspend fun dismiss() {
        val tag = shown.value?.tag ?: return
        repo.dismissUpdate(tag)
        shown.value = null
    }

    /** Starts the APK download of the release found; the state follows in [downloadState]. */
    fun startDownload() {
        val release = found.value ?: return
        val d = downloads ?: return
        if (download.value == Download.Running) return
        d.ready(release)?.let {
            download.value = Download.Ready(it)
            return
        }
        download.value = Download.Running
        scope.launch {
            val id = d.enqueue(release)
            download.value = when (val p = id?.let { d.awaitFinish(it) }) {
                is ApkDownloads.Progress.Done -> Download.Ready(p.apk)
                else -> Download.Failed
            }
        }
    }

    companion object {
        private const val TIMEOUT_MS = 15_000
        private const val MAX_CHARS = 256 * 1024

        /**
         * One anonymous GET of the public release document: no token, no account, nothing about
         * the relay or its hubs. Null on any failure (404 until a release exists, 403 when the
         * anonymous hourly limit is spent, no network).
         */
        suspend fun fetchLatest(): String? = withContext(Dispatchers.IO) {
            var c: HttpURLConnection? = null
            try {
                c = (URI(Updates.LATEST_RELEASE_URL).toURL().openConnection() as HttpURLConnection).apply {
                    requestMethod = "GET"
                    connectTimeout = TIMEOUT_MS
                    readTimeout = TIMEOUT_MS
                    setRequestProperty("Accept", "application/vnd.github+json")
                }
                if (c.responseCode != HttpURLConnection.HTTP_OK) return@withContext null
                val text = StringBuilder()
                c.inputStream.bufferedReader().use { r ->
                    val buf = CharArray(8 * 1024)
                    while (text.length < MAX_CHARS) {
                        val n = r.read(buf)
                        if (n < 0) break
                        text.appendRange(buf, 0, n)
                    }
                }
                text.toString()
            } catch (_: IOException) {
                null
            } finally {
                c?.disconnect()
            }
        }
    }
}
