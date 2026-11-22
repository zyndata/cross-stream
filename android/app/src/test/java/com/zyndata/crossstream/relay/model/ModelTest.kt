package com.zyndata.crossstream.relay.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ModelTest {
    @Test
    fun qrInfoFromCoreJson() {
        val q = QrInfo.fromJson("""{"hub":"dell","hpk":"k=","ssid":"CrossStream-dell","bssid":"02:11:22:33:44:55","psk":"pw"}""")
        assertEquals(QrInfo("dell", "k=", "CrossStream-dell", "02:11:22:33:44:55", "pw"), q)
    }

    @Test
    fun qrInfoWithoutBssid() {
        assertEquals("", QrInfo.fromJson("""{"hub":"h","hpk":"k","ssid":"s","bssid":"","psk":"p"}""").bssid)
    }

    @Test(expected = org.json.JSONException::class)
    fun qrInfoNeedsHubKey() {
        QrInfo.fromJson("""{"hub":"h","ssid":"s","bssid":"","psk":"p"}""")
    }

    @Test
    fun statsFromCoreJson() {
        val s = RelayStats.fromJson(
            """{"running":true,"connected":true,"hub":"dell","relay_id":"r1","path_id":2,"bytes_up":1500000,
               "bytes_down":500000,"period_used":0,"dropped":3,"proxy_flows":1,"wifi":true,"cellular":false}""",
        )
        assertTrue(s.running && s.connected && s.wifi)
        assertFalse(s.cellular)
        assertEquals("r1", s.relayId)
        assertEquals(2, s.pathId)
        assertEquals(2_000_000L, s.sharedBytes)
        assertEquals(3L, s.dropped)
        assertEquals("", s.lastError)
    }

    @Test
    fun statsIdle() {
        val s = RelayStats.fromJson("""{"running":false,"connected":false,"bytes_up":0,"bytes_down":0,"last_error":"rejected: hub full"}""")
        assertFalse(s.running)
        assertEquals("rejected: hub full", s.lastError)
        assertEquals(RelayStats(lastError = "rejected: hub full"), s)
    }

    @Test
    fun formatMegabytes() {
        val cases = mapOf(0L to "0.0 MB", 49_999L to "0.0 MB", 1_250_000L to "1.3 MB", 12_345_678_901L to "12345.7 MB")
        cases.forEach { (bytes, want) -> assertEquals(want, formatMb(bytes)) }
        val data = mapOf(0L to "0.0 MB", 999_949_999L to "999.9 MB", 1_000_000_000L to "1.00 GB", 2_345_678_901L to "2.35 GB")
        data.forEach { (bytes, want) -> assertEquals(want, formatData(bytes)) }
    }
}
