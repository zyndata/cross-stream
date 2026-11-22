package com.zyndata.crossstream.relay.logic

/** Removes secrets from log lines (same rules as the Go core's Redact). */
object Redact {
    private val code = Regex("""XS[RE]1\.[A-Za-z0-9_\-=+/]+""")
    private val secret = Regex("""(?i)("?(?:psk|password|passphrase|tok|token|pair_token)"?\s*[=:]\s*"?)[^"\s,}]+""")

    fun line(s: String): String = secret.replace(code.replace(s, "<code>")) { it.groupValues[1] + "<redacted>" }
}
