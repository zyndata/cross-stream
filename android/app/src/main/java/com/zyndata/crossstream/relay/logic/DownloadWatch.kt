package com.zyndata.crossstream.relay.logic

/**
 * Follows one APK download by polling (DownloadManager). Pure, so JVM tests cover it.
 *
 * A download that moves no byte for [stallPolls] polls in a row is given up: DownloadManager
 * keeps a transfer that waits for a network (`STATUS_PAUSED`) forever, and the app would keep
 * polling it for as long as the relay keeps the process alive. Polls only run while the
 * phone is awake, so time asleep (Doze) never counts towards a stall.
 */
class DownloadWatch(private val stallPolls: Int) {
    /** What DownloadManager says about the transfer; null when it is gone (canceled). */
    data class Snapshot(val status: Status, val bytes: Long)

    enum class Status { WAITING, RUNNING, PAUSED, SUCCESSFUL, FAILED }

    sealed interface Verdict {
        data object Wait : Verdict
        data object Done : Verdict
        data object Failed : Verdict
        /** Nothing moved for too long: the caller removes the transfer. */
        data object Stalled : Verdict
    }

    private var lastBytes = -1L
    private var still = 0

    fun next(s: Snapshot?): Verdict {
        when (s?.status) {
            null, Status.FAILED -> return Verdict.Failed
            Status.SUCCESSFUL -> return Verdict.Done
            else -> Unit
        }
        if (s.bytes != lastBytes) {
            lastBytes = s.bytes
            still = 0
            return Verdict.Wait
        }
        still++
        return if (still >= stallPolls) Verdict.Stalled else Verdict.Wait
    }
}
