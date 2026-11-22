package com.zyndata.crossstream.relay.logic

import com.zyndata.crossstream.relay.logic.DownloadWatch.Snapshot
import com.zyndata.crossstream.relay.logic.DownloadWatch.Status
import com.zyndata.crossstream.relay.logic.DownloadWatch.Verdict
import org.junit.Assert.assertEquals
import org.junit.Test

class DownloadWatchTest {
    private fun run(stallPolls: Int, vararg polls: Snapshot?): List<Verdict> {
        val w = DownloadWatch(stallPolls)
        return polls.map { w.next(it) }
    }

    @Test
    fun verdicts() {
        data class Case(val name: String, val polls: List<Snapshot?>, val want: Verdict)
        val cases = listOf(
            Case("finished", listOf(Snapshot(Status.RUNNING, 10), Snapshot(Status.SUCCESSFUL, 30)), Verdict.Done),
            Case("failed (server, storage)", listOf(Snapshot(Status.RUNNING, 10), Snapshot(Status.FAILED, 10)), Verdict.Failed),
            Case("canceled from the notification", listOf(Snapshot(Status.RUNNING, 10), null), Verdict.Failed),
            Case(
                "waiting for a network that never comes",
                listOf(Snapshot(Status.RUNNING, 10), Snapshot(Status.PAUSED, 10), Snapshot(Status.PAUSED, 10), Snapshot(Status.PAUSED, 10)),
                Verdict.Stalled,
            ),
            Case(
                "never started (queued behind others)",
                listOf(Snapshot(Status.WAITING, 0), Snapshot(Status.WAITING, 0), Snapshot(Status.WAITING, 0), Snapshot(Status.WAITING, 0)),
                Verdict.Stalled,
            ),
            Case(
                "slow but moving",
                listOf(Snapshot(Status.RUNNING, 1), Snapshot(Status.RUNNING, 1), Snapshot(Status.RUNNING, 2), Snapshot(Status.RUNNING, 2), Snapshot(Status.RUNNING, 3)),
                Verdict.Wait,
            ),
            Case(
                "paused a while (screen off, network back), then moving again",
                listOf(Snapshot(Status.PAUSED, 5), Snapshot(Status.PAUSED, 5), Snapshot(Status.PAUSED, 5), Snapshot(Status.RUNNING, 6)),
                Verdict.Wait,
            ),
        )
        for (c in cases) {
            assertEquals(c.name, c.want, run(3, *c.polls.toTypedArray()).last())
        }
    }

    @Test
    fun aStallNeedsThatManyStillPollsInARow() {
        val still = Snapshot(Status.PAUSED, 7)
        val verdicts = run(3, still, still, still, still)
        assertEquals(listOf(Verdict.Wait, Verdict.Wait, Verdict.Wait, Verdict.Stalled), verdicts)
    }
}
