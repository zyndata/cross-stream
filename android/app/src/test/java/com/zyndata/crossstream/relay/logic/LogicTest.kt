package com.zyndata.crossstream.relay.logic

import java.time.LocalDateTime
import java.time.ZoneId
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UsageTest {
    private val zone = ZoneId.of("Europe/Warsaw")
    private fun ms(s: String) = LocalDateTime.parse(s).atZone(zone).toInstant().toEpochMilli()

    @Test
    fun periodStart() {
        val session = ms("2026-11-10T08:00:00")
        data class Case(val s: RelaySettings, val now: String, val want: Long)
        val cases = listOf(
            Case(RelaySettings(period = Period.SESSION), "2026-11-13T12:00:00", session),
            Case(RelaySettings(period = Period.DAY), "2026-11-13T12:00:00", ms("2026-11-13T00:00:00")),
            Case(RelaySettings(period = Period.DAY), "2026-11-13T00:00:00", ms("2026-11-13T00:00:00")),
            Case(RelaySettings(period = Period.MONTH, resetDay = 1), "2026-11-13T12:00:00", ms("2026-11-01T00:00:00")),
            Case(RelaySettings(period = Period.MONTH, resetDay = 15), "2026-11-13T12:00:00", ms("2026-10-15T00:00:00")),
            Case(RelaySettings(period = Period.MONTH, resetDay = 15), "2026-11-15T00:00:00", ms("2026-11-15T00:00:00")),
            Case(RelaySettings(period = Period.MONTH, resetDay = 10), "2026-01-05T12:00:00", ms("2025-12-10T00:00:00")),
            Case(RelaySettings(period = Period.MONTH, resetDay = 0), "2026-11-13T12:00:00", ms("2026-11-01T00:00:00")),
        )
        for (c in cases) {
            assertEquals("${c.s.period} ${c.s.resetDay} ${c.now}", c.want, Usage.periodStart(c.s, ms(c.now), session, zone))
        }
    }

    @Test
    fun dailyLimitRollsAtMidnight() {
        val s = RelaySettings(capMb = 50, period = Period.DAY)
        var u = Usage().add(30_000_000, s, ms("2026-11-13T22:00:00"), 0, zone)
        u = u.add(25_000_000, s, ms("2026-11-13T23:59:00"), 0, zone)
        assertTrue(u.overLimit(s))
        assertEquals(55_000_000, u.used)
        u = u.roll(s, ms("2026-11-14T00:00:01"), 0, zone)
        assertFalse(u.overLimit(s))
        assertEquals(0, u.used)
        assertEquals(55_000_000, u.allTime)
        u = u.add(1_000, s, ms("2026-11-14T08:00:00"), 0, zone)
        assertEquals(1_000, u.used)
        assertEquals(55_001_000, u.allTime)
    }

    @Test
    fun sessionPeriodRollsOnNewSession() {
        val s = RelaySettings(capMb = 10, period = Period.SESSION)
        var u = Usage().add(5, s, 2_000, 1_000, zone)
        assertEquals(5, u.used)
        u = u.add(5, s, 3_000, 1_000, zone) // same session
        assertEquals(10, u.used)
        u = u.add(1, s, 9_000, 8_000, zone) // toggle switched on again at 8 000
        assertEquals(1, u.used)
        assertEquals(11, u.allTime)
    }

    @Test
    fun resetAndUnlimited() {
        val s = RelaySettings(capMb = 1, period = Period.MONTH)
        val u = Usage(periodStart = 1, used = 2_000_000, allTime = 9_000_000)
        assertTrue(u.overLimit(s))
        assertFalse(u.overLimit(s.copy(capMb = 0)))
        val r = u.reset(5_000)
        assertEquals(Usage(5_000, 0, 9_000_000), r)
        // A reset mid-month is not undone by the next roll.
        assertEquals(r, r.roll(s, 6_000, 0, zone))
    }
}

class SettingsTest {
    @Test
    fun limitsJson() {
        val unlimited = JSONObject(RelaySettings(capMb = 0).limitsJson(123_000_000))
        assertEquals(0, unlimited.length())

        val month = JSONObject(RelaySettings(capMb = 2_000, period = Period.MONTH, resetDay = 15).limitsJson(12_345_678))
        assertEquals(2_000, month.getLong("cap_mb"))
        assertEquals("month", month.getString("cap_period"))
        assertEquals(15, month.getInt("reset_day"))
        assertEquals(12, month.getLong("used_mb"))
        assertEquals("stop", month.getString("over_cap"))
        assertFalse(month.has("throttle_kbps"))
        assertFalse(month.has("battery_min"))

        val day = JSONObject(RelaySettings(capMb = 50, period = Period.DAY, overLimit = OverLimit.THROTTLE).limitsJson(0))
        assertEquals("day", day.getString("cap_period"))
        assertFalse(day.has("reset_day"))
        assertFalse(day.has("used_mb"))
        assertEquals("throttle", day.getString("over_cap"))
        assertEquals(256, day.getInt("throttle_kbps"))
        assertFalse(day.has("max_priority"))

        val backup = JSONObject(RelaySettings(capMb = 0).limitsJson(0, HubUse.BACKUP_ONLY))
        assertEquals("backup", backup.getString("max_priority"))
        assertEquals("secondary", JSONObject(RelaySettings().limitsJson(0, HubUse.WHEN_NEEDED)).getString("max_priority"))
    }

    @Test
    fun themeMode() {
        assertEquals(ThemeMode.SYSTEM, ThemeMode.of(null))
        assertEquals(ThemeMode.DARK, ThemeMode.of("dark"))
        assertTrue(ThemeMode.SYSTEM.dark(systemDark = true))
        assertFalse(ThemeMode.SYSTEM.dark(systemDark = false))
        assertFalse(ThemeMode.LIGHT.dark(systemDark = true))
        assertTrue(ThemeMode.DARK.dark(systemDark = false))
    }

    @Test
    fun hubUseRoundTrip() {
        val h = SavedHub(id = "a", name = "a", ssid = "s", bssid = "", qr = "XSR1.a", use = HubUse.WHEN_NEEDED)
        assertEquals(HubUse.WHEN_NEEDED, SavedHub.fromJson(h.toJson()).use)
        assertEquals(HubUse.NORMAL, SavedHub.fromJson(h.toJson().apply { remove("use") }).use)
        assertEquals(HubUse.NORMAL, HubUse.of("bogus"))
    }

    @Test
    fun enumsAndPresets() {
        assertEquals(Period.DAY, Period.of("day"))
        assertEquals(Period.MONTH, Period.of("bogus"))
        assertEquals(OverLimit.THROTTLE, OverLimit.of("throttle"))
        assertEquals(OverLimit.STOP, OverLimit.of(null))
        assertEquals(200, RelaySettings.suggestedDailyMb(2_000))
        assertEquals(listOf(500L, 1_000L, 2_000L, 5_000L), RelaySettings.PRESETS_MB)
    }
}

class StopPolicyTest {
    private val now = 1_000_000L

    @Test
    fun stopReasons() {
        data class Case(val name: String, val s: RelaySettings, val u: Usage, val d: DeviceState, val want: String?)
        val s = RelaySettings(capMb = 50, batteryMin = 20)
        val over = Usage(used = 50_000_000)
        val cases = listOf(
            Case("all fine", s, Usage(), DeviceState(), null),
            Case("limit stop", s, over, DeviceState(), StopPolicy.LIMIT),
            Case("limit throttle", s.copy(overLimit = OverLimit.THROTTLE), over, DeviceState(), null),
            Case("unlimited", s.copy(capMb = 0), over, DeviceState(), null),
            Case("battery low", s, Usage(), DeviceState(battery = 19), StopPolicy.BATTERY),
            Case("battery at threshold", s, Usage(), DeviceState(battery = 20), null),
            Case("battery low charging, stop anyway", s, Usage(), DeviceState(battery = 10, charging = true), StopPolicy.BATTERY),
            Case("battery low charging, allowed", s.copy(batteryStopWhenCharging = false), Usage(),
                DeviceState(battery = 10, charging = true), null),
            Case("battery threshold off", s.copy(batteryMin = 0), Usage(), DeviceState(battery = 1), null),
            Case("no data in grace", s, Usage(), DeviceState(cellularValidated = false, cellularBadSince = now - 29_000), null),
            Case("no data", s, Usage(), DeviceState(cellularValidated = false, cellularBadSince = now - 30_000), StopPolicy.NO_DATA),
            Case("limit first", s, over, DeviceState(battery = 1), StopPolicy.LIMIT),
        )
        for (c in cases) assertEquals(c.name, c.want, StopPolicy.stopReason(c.s, c.u, c.d, now))
    }

    @Test
    fun resume() {
        val s = RelaySettings(capMb = 50, batteryMin = 20)
        val b = StopPolicy.BATTERY
        assertFalse(StopPolicy.canResume(b, s, Usage(), DeviceState(battery = 22), now))
        assertTrue(StopPolicy.canResume(b, s, Usage(), DeviceState(battery = 25), now))
        assertFalse(StopPolicy.canResume(b, s, Usage(), DeviceState(battery = 22, charging = true), now))
        assertTrue(StopPolicy.canResume(b, s.copy(batteryStopWhenCharging = false), Usage(), DeviceState(battery = 5, charging = true), now))
        assertTrue(StopPolicy.canResume(b, s.copy(batteryMin = 98), Usage(), DeviceState(battery = 100), now))
        // battery fine but over the limit: still paused
        assertFalse(StopPolicy.canResume(b, s, Usage(used = 60_000_000), DeviceState(battery = 90), now))

        val l = StopPolicy.LIMIT
        assertFalse(StopPolicy.canResume(l, s, Usage(used = 50_000_000), DeviceState(), now))
        assertTrue(StopPolicy.canResume(l, s, Usage(used = 1), DeviceState(), now))
        assertTrue(StopPolicy.canResume(l, s.copy(capMb = 100), Usage(used = 50_000_000), DeviceState(), now))

        val n = StopPolicy.NO_DATA
        assertFalse(StopPolicy.canResume(n, s, Usage(), DeviceState(cellularValidated = false, cellularBadSince = now), now))
        assertTrue(StopPolicy.canResume(n, s, Usage(), DeviceState(), now))
    }
}

class HubsTest {
    private fun hub(id: String, bssid: String, lastSeen: Long = 0, ssid: String = "CS-$id") =
        SavedHub(id = id, name = id, ssid = ssid, bssid = bssid, qr = "XSR1.$id", lastSeen = lastSeen)

    @Test
    fun jsonRoundTrip() {
        val hubs = listOf(hub("a", "02:00:00:00:00:01", 5).copy(sharedBytes = 77), hub("b", ""))
        assertEquals(hubs, SavedHub.listFromJson(SavedHub.listToJson(hubs)))
        assertEquals(emptyList<SavedHub>(), SavedHub.listFromJson(null))
        assertEquals(emptyList<SavedHub>(), SavedHub.listFromJson("not json"))
        assertEquals(1, SavedHub.listFromJson("""[{"id":"x","qr":"q"},{"broken":1}]""").size)
    }

    @Test
    fun idOf() {
        val id = SavedHub.idOf("hpk-base64")
        assertEquals(16, id.length)
        assertTrue(id.all { it in "0123456789abcdef" })
        assertEquals(id, SavedHub.idOf("hpk-base64"))
        assertFalse(id == SavedHub.idOf("other"))
    }

    @Test
    fun pick() {
        val a = hub("a", "02:00:00:00:00:01", lastSeen = 10)
        val b = hub("b", "02:00:00:00:00:02", lastSeen = 20)
        val c = hub("c", "", ssid = "Plain")
        val hubs = listOf(a, b, c)
        val scan = listOf(
            SeenAp("02:00:00:00:00:01", "CS-a", -70),
            SeenAp("02:00:00:00:00:02", "CS-b", -50),
            SeenAp("02:00:00:00:00:99", "Other", -30),
        )
        data class Case(val name: String, val scan: List<SeenAp>?, val cool: Map<String, Long>, val want: SavedHub?)
        val cases = listOf(
            Case("strongest", scan, emptyMap(), b),
            Case("bssid case-insensitive", listOf(SeenAp("02:00:00:00:00:01".uppercase(), "x", -80)), emptyMap(), a),
            Case("cooling down skipped", scan, mapOf("b" to 200L), a),
            Case("cooldown over", scan, mapOf("b" to 100L), b),
            Case("ssid match without bssid", listOf(SeenAp("aa:bb:cc:dd:ee:ff", "Plain", -60)), emptyMap(), c),
            Case("none visible", listOf(SeenAp("02:00:00:00:00:99", "Other", -30)), emptyMap(), null),
            Case("no scan: most recent", null, emptyMap(), b),
            Case("no scan: most recent not cooling", null, mapOf("b" to 200L), a),
        )
        for (cs in cases) assertEquals(cs.name, cs.want, HubPicker.pick(hubs, cs.scan, cs.cool, 100))
        assertNull(HubPicker.pick(emptyList(), scan, emptyMap(), 0))
        assertEquals(setOf("a", "b"), HubPicker.inRange(hubs, scan))
    }

    @Test
    fun unavailableCooldown() {
        val cases = listOf(
            Triple("declined at once", 3_000L, HubPicker.COOLDOWN_MS),
            Triple("declined after a while", 30_000L, HubPicker.COOLDOWN_MS),
            Triple("not found: at the timeout", 45_000L, HubPicker.NOT_FOUND_COOLDOWN_MS),
            Triple("not found: just before (callback jitter)", 41_000L, HubPicker.NOT_FOUND_COOLDOWN_MS),
            Triple("not found: noticed late", 60_000L, HubPicker.NOT_FOUND_COOLDOWN_MS),
        )
        for ((name, elapsed, want) in cases) assertEquals(name, want, HubPicker.unavailableCooldown(elapsed, 45_000L))
    }

    @Test
    fun wakeFor() {
        val a = hub("a", "02:00:00:00:00:01")
        val seen = listOf(SeenAp("02:00:00:00:00:01", "CS-a", -70))
        val other = listOf(SeenAp("02:00:00:00:00:99", "Other", -30))
        data class Case(val name: String, val scan: List<SeenAp>?, val cool: Map<String, Long>, val want: Boolean)
        val cases = listOf(
            Case("saved hub in new results", seen, emptyMap(), true),
            Case("only other networks", other, emptyMap(), false),
            Case("hub cooling down", seen, mapOf("a" to 200L), false),
            Case("results unknown: no wake", null, emptyMap(), false),
        )
        for (cs in cases) assertEquals(cs.name, cs.want, HubPicker.wakeFor(listOf(a), cs.scan, cs.cool, 100))
        assertFalse(HubPicker.wakeFor(emptyList(), seen, emptyMap(), 0))
    }

    @Test
    fun latestScanDropsEarlierScans() {
        val now = 500_000_000L
        val fresh = SeenAp("02:00:00:00:00:01", "a", -50, now)
        val sameScan = SeenAp("02:00:00:00:00:02", "b", -60, now - HubPicker.STALE_US)
        val old = SeenAp("02:00:00:00:00:03", "switched-off hub", -40, now - HubPicker.STALE_US - 1)
        val unknownAge = SeenAp("02:00:00:00:00:04", "d", -70, 0)
        assertEquals(listOf(fresh, sameScan, unknownAge), HubPicker.latestScan(listOf(fresh, sameScan, old, unknownAge)))
        assertEquals(emptyList<SeenAp>(), HubPicker.latestScan(emptyList()))
    }

    @Test
    fun pickChosenHub() {
        val a = hub("a", "02:00:00:00:00:01", lastSeen = 10).copy(preferred = true)
        val b = hub("b", "02:00:00:00:00:02", lastSeen = 20)
        val hubs = listOf(a, b)
        val both = listOf(SeenAp("02:00:00:00:00:01", "CS-a", -80), SeenAp("02:00:00:00:00:02", "CS-b", -40))
        val onlyB = listOf(SeenAp("02:00:00:00:00:02", "CS-b", -40))
        data class Case(val name: String, val scan: List<SeenAp>?, val cool: Map<String, Long>, val want: SavedHub?, val leaveB: Boolean)
        val cases = listOf(
            Case("chosen beats a stronger hub", both, emptyMap(), a, true),
            Case("chosen out of range: strongest", onlyB, emptyMap(), b, false),
            Case("no scan: chosen, not the most recent", null, emptyMap(), a, true),
            Case("chosen cooling down: another hub", null, mapOf("a" to 200L), b, false),
        )
        for (cs in cases) {
            assertEquals(cs.name, cs.want, HubPicker.pick(hubs, cs.scan, cs.cool, 100))
            assertEquals(cs.name + " (switch)", cs.leaveB, HubPicker.switchTo(hubs, "b", cs.scan, cs.cool, 100))
        }
        assertFalse("already on the chosen hub", HubPicker.switchTo(hubs, "a", both, emptyMap(), 100))
        assertFalse("nothing chosen", HubPicker.switchTo(listOf(b), "b", null, emptyMap(), 100))
        assertEquals(true, SavedHub.fromJson(a.toJson()).preferred)
    }

    @Test
    fun hubLabel() {
        val h = hub("a", "02:00:00:00:00:01")
        data class Case(val label: String, val clean: String, val shown: String)
        for (cs in listOf(
            Case("", "", "a"),
            Case("  ", "", "a"),
            Case(" Lenovo w domu ", "Lenovo w domu", "Lenovo w domu"),
            Case("x".repeat(40), "x".repeat(SavedHub.LABEL_MAX), "x".repeat(SavedHub.LABEL_MAX)),
        )) {
            val l = SavedHub.cleanLabel(cs.label)
            assertEquals(cs.label, cs.clean, l)
            val withLabel = h.copy(label = l)
            assertEquals(cs.label, cs.shown, withLabel.shownName)
            assertEquals("json round trip", withLabel, SavedHub.fromJson(withLabel.toJson()))
        }
    }

    @Test
    fun sessionScanAndSwitchOffer() {
        val a = hub("a", "02:00:00:00:00:01").copy(preferred = true)
        val b = hub("b", "02:00:00:00:00:02")
        val onlyB = listOf(SeenAp("02:00:00:00:00:02", "CS-b", -40))
        val both = onlyB + SeenAp("02:00:00:00:00:01", "CS-a", -60)
        // The session started with a scan without the chosen hub; a later scan has it.
        assertFalse("start scan: chosen away", HubPicker.switchTo(listOf(a, b), "b", onlyB, emptyMap(), 100))
        val later = HubPicker.sessionScan(onlyB, both)
        assertEquals(both, later)
        assertTrue("later scan: switch", HubPicker.switchTo(listOf(a, b), "b", later, emptyMap(), 100))
        assertEquals("empty results keep the last scan", onlyB, HubPicker.sessionScan(onlyB, emptyList()))
        assertEquals("no results keep the last scan", onlyB, HubPicker.sessionScan(onlyB, null))
        assertEquals(null, HubPicker.sessionScan(null, null))

        data class Case(val name: String, val hubs: List<SavedHub>, val current: String, val inSession: Boolean, val want: SavedHub?)
        for (cs in listOf(
            Case("sharing with another hub", listOf(a, b), "b", true, a),
            Case("already on the chosen hub", listOf(a, b), "a", true, null),
            Case("nothing chosen", listOf(b, a.copy(preferred = false)), "b", true, null),
            Case("not in a session", listOf(a, b), "b", false, null),
            Case("no current hub", listOf(a, b), "", true, null),
        )) {
            assertEquals(cs.name, cs.want, HubPicker.switchOffer(cs.hubs, cs.current, cs.inSession))
        }
    }

    @Test
    fun backoff() {
        assertEquals(listOf(5_000L, 10_000L, 20_000L, 40_000L, 60_000L, 60_000L), (0..5).map(HubPicker::backoff))
    }
}

class RedactTest {
    @Test
    fun line() {
        val cases = mapOf(
            "scanned XSR1.eyJ2IjoxfQ ok" to "scanned <code> ok",
            "psk=secretsecret ssid=x" to "psk=<redacted> ssid=x",
            """{"psk":"secret","ssid":"x"}""" to """{"psk":"<redacted>","ssid":"x"}""",
            "pair_token=abc token: def" to "pair_token=<redacted> token: <redacted>",
            "relay connected hub=dell" to "relay connected hub=dell",
        )
        for ((input, want) in cases) assertEquals(input, want, Redact.line(input))
    }
}
