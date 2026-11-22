package com.zyndata.crossstream.relay.logic

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdatesTest {
    private fun doc(
        tag: String = "v1.2.0",
        url: String = "https://github.com/zyndata/cross-stream/releases/download/v1.2.0/crossstream-relay-v1.2.0.apk",
        name: String = "crossstream-relay-v1.2.0.apk",
        extra: String = "",
    ) = """{"tag_name":"$tag","html_url":"https://github.com/zyndata/cross-stream/releases/tag/$tag"$extra,
        "assets":[{"name":"crossstream-hub-$tag-linux-amd64.tar.gz","size":5,
        "browser_download_url":"https://github.com/zyndata/cross-stream/releases/download/$tag/x.tar.gz"},
        {"name":"$name","size":31000000,"browser_download_url":"$url"}]}"""

    @Test
    fun version() {
        data class Case(val text: String?, val want: Updates.Version?)
        val cases = listOf(
            Case("v1.2.3", Updates.Version(1, 2, 3)),
            Case("1.0.0", Updates.Version(1, 0, 0)),
            Case("0.0.0-dev", null),
            Case("v1.0.0-dry.abc", null),
            Case("", null),
            Case(null, null),
        )
        for (c in cases) assertEquals(c.text, c.want, Updates.version(c.text))
        assertTrue(Updates.Version(1, 9, 9) < Updates.Version(2, 0, 0))
        assertEquals("v1.2.3", Updates.Version(1, 2, 3).toString())
    }

    @Test
    fun offer() {
        data class Case(val name: String, val document: String, val installed: String?, val offered: Boolean)
        val cases = listOf(
            Case("newer", doc(), "1.1.0", true),
            Case("same", doc(), "1.2.0", false),
            Case("older release", doc(), "1.3.0", false),
            Case("dev build", doc(), "0.0.0-dev", false),
            Case("apk elsewhere", doc(url = "https://example.com/crossstream-relay-v1.2.0.apk"), "1.1.0", false),
            Case("no apk", doc(name = "notes.txt"), "1.1.0", false),
            Case("prerelease", doc(extra = ""","prerelease":true"""), "1.1.0", false),
            Case("not json", "<html>", "1.1.0", false),
        )
        for (c in cases) assertEquals(c.name, c.offered, Updates.offer(c.document, c.installed) != null)
    }

    @Test
    fun parse() {
        val r = Updates.parse(doc())
        assertNotNull(r)
        assertEquals("v1.2.0", r!!.tag)
        assertEquals(31_000_000L, r.apkSize)
        assertTrue(r.apkUrl.endsWith("crossstream-relay-v1.2.0.apk"))
        assertNull(Updates.parse("""{"tag_name":"nightly","assets":[]}"""))
    }

    @Test
    fun staleApk() {
        assertTrue(Updates.isStaleApk("crossstream-relay-v1.2.0.apk", "1.2.0"))
        assertTrue(Updates.isStaleApk("crossstream-relay-v1.1.9.apk", "1.2.0"))
        assertFalse(Updates.isStaleApk("crossstream-relay-v1.3.0.apk", "1.2.0"))
        assertFalse(Updates.isStaleApk("other.apk", "1.2.0"))
        assertFalse(Updates.isStaleApk("crossstream-relay-v1.0.0.apk", "0.0.0-dev"))
        assertEquals("crossstream-relay-v1.2.0.apk", Updates.apkName(Updates.Version(1, 2, 0)))
    }

    @Test
    fun aDownloadedApkIsReusedOnlyWhenWhole() {
        data class Case(val name: String, val length: Long, val size: Long, val want: Boolean)
        val cases = listOf(
            Case("whole", 30_000_000, 30_000_000, true),
            Case("cut off (app killed, network lost)", 12_345, 30_000_000, false),
            Case("release does not say its size", 30_000_000, 0, false),
            Case("empty file", 0, 30_000_000, false),
        )
        for (c in cases) assertEquals(c.name, c.want, Updates.isComplete(c.length, c.size))
    }
}
