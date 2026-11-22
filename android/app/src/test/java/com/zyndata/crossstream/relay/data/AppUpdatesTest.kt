package com.zyndata.crossstream.relay.data

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AppUpdatesTest {
    private class MemoryStore(auto: Boolean = true) : AppUpdates.Store {
        val auto = MutableStateFlow(auto)
        var dismissed: String? = null
        override val updateAuto: Flow<Boolean> = this.auto
        override suspend fun updateDismissed(): String? = dismissed
        override suspend fun dismissUpdate(tag: String) {
            dismissed = tag
        }
    }

    private fun release(tag: String) = """{"tag_name":"$tag","assets":[{"name":"crossstream-relay-$tag.apk",
        "size":30000000,"browser_download_url":"https://github.com/zyndata/cross-stream/releases/download/$tag/crossstream-relay-$tag.apk"}]}"""

    /** An AppUpdates whose GitHub answers [documents] in turn and counts the requests. */
    private class Fixture(installed: String, auto: Boolean = true, vararg documents: String?) {
        val store = MemoryStore(auto)
        var requests = 0
        private val answers = documents.toMutableList()
        val updates = AppUpdates(store, null, CoroutineScope(Dispatchers.Unconfined), installed) {
            requests++
            answers.removeFirstOrNull()
        }
    }

    @Test
    fun checkAtStartFollowsTheSwitch() = runBlocking {
        data class Case(val name: String, val installed: String, val auto: Boolean, val wantRequests: Int)
        val cases = listOf(
            Case("on", "1.0.0", true, 1),
            Case("switched off", "1.0.0", false, 0),
            Case("development build", "0.0.0-dev", true, 0),
        )
        for (c in cases) {
            val f = Fixture(c.installed, c.auto, release("v1.0.1"))
            f.updates.checkAtStart()
            assertEquals(c.name, c.wantRequests, f.requests)
        }
    }

    @Test
    fun outcomes() = runBlocking {
        data class Case(val name: String, val installed: String, val document: String?, val want: (AppUpdates.Outcome) -> Boolean)
        val cases = listOf(
            Case("newer", "1.0.0", release("v1.0.1")) { it is AppUpdates.Outcome.Newer && it.release.tag == "v1.0.1" },
            Case("same", "1.0.1", release("v1.0.1")) { it == AppUpdates.Outcome.UpToDate("v1.0.1") },
            Case("unreachable", "1.0.0", null) { it == AppUpdates.Outcome.Unreachable },
            Case("dev build sees the latest tag", "0.0.0-dev", release("v1.0.1")) { it == AppUpdates.Outcome.UpToDate("v1.0.1") },
            Case("Wi-Fi login page instead of GitHub", "1.0.0", "<html><body>Log in</body></html>") { it == AppUpdates.Outcome.Unreachable },
            Case("JSON error page", "1.0.0", """{"message":"Not Found"}""") { it == AppUpdates.Outcome.Unreachable },
            Case("pre-release only", "1.0.0", """{"tag_name":"v2.0.0","prerelease":true}""") { it == AppUpdates.Outcome.Unreachable },
            Case("release without an APK", "1.0.0", """{"tag_name":"v1.0.1","assets":[]}""") { it == AppUpdates.Outcome.UpToDate("v1.0.1") },
        )
        for (c in cases) {
            val f = Fixture(c.installed, true, c.document)
            val o = f.updates.check()
            assertTrue("${c.name}: $o", c.want(o))
        }
    }

    @Test
    fun hideKeepsTheReleaseInSettingsUntilANewerOne() = runBlocking {
        val f = Fixture("1.0.0", true, release("v1.0.1"), release("v1.0.1"), release("v1.0.2"))
        f.updates.check()
        assertNotNull(f.updates.banner.value)
        f.updates.dismiss()
        assertEquals("v1.0.1", f.store.dismissed)
        assertNull(f.updates.banner.value)
        assertNotNull("Settings still offers it", f.updates.latest.value)

        f.updates.check() // the same version again: stays hidden on Home
        assertNull(f.updates.banner.value)

        f.updates.check() // a newer one shows again
        assertEquals("v1.0.2", f.updates.banner.value?.tag)
    }

    @Test
    fun unreachableKeepsWhatWasFound() = runBlocking {
        val f = Fixture("1.0.0", true, release("v1.0.1"), null)
        f.updates.check()
        assertEquals(AppUpdates.Outcome.Unreachable, f.updates.check())
        assertEquals("v1.0.1", f.updates.latest.value?.tag)
    }

    @Test
    fun aLoginPageKeepsWhatWasFound() = runBlocking {
        val f = Fixture("1.0.0", true, release("v1.0.1"), "<html>Hotel Wi-Fi</html>")
        f.updates.check()
        assertEquals(AppUpdates.Outcome.Unreachable, f.updates.check())
        assertEquals("v1.0.1", f.updates.banner.value?.tag)
    }

    @Test
    fun checksOneAtATime() = runBlocking {
        // "Check now" pressed while the check at start still runs: both finish, in turn.
        var inFlight = 0
        var most = 0
        val store = MemoryStore()
        val updates = AppUpdates(store, null, CoroutineScope(Dispatchers.Unconfined), "1.0.0") {
            inFlight++
            most = maxOf(most, inFlight)
            kotlinx.coroutines.delay(50)
            inFlight--
            release("v1.0.1")
        }
        val a = launch(Dispatchers.Default) { updates.check() }
        val b = launch(Dispatchers.Default) { updates.check() }
        a.join()
        b.join()
        assertEquals(1, most)
        assertEquals("v1.0.1", updates.latest.value?.tag)
    }

    @Test
    fun downloadWithoutDownloadManagerDoesNothing() {
        val f = Fixture("1.0.0", true)
        f.updates.startDownload()
        assertEquals(AppUpdates.Download.Idle, f.updates.downloadState.value)
    }
}
