package com.zyndata.crossstream.relay.data

import android.util.Log
import com.zyndata.crossstream.relay.logic.Redact
import java.io.File
import java.time.LocalDateTime
import java.time.format.DateTimeFormatter

/**
 * App log: logcat plus a rolling, redacted files/logs/app.log (one old copy,
 * 256 KB each). The Go core writes core.log next to it.
 */
class AppLog(private val dir: File) {
    private val file = File(dir, FILE)

    fun i(tag: String, msg: String) {
        Log.i(tag, msg)
        write("I", tag, msg)
    }

    fun w(tag: String, msg: String, t: Throwable? = null) {
        Log.w(tag, msg, t)
        write("W", tag, if (t == null) msg else "$msg: $t")
    }

    /** Files to attach to "Share logs" (existing ones only). */
    fun files(): List<File> = listOf(FILE, "$FILE.1", CORE, "$CORE.1").map { File(dir, it) }.filter { it.isFile }

    @Synchronized
    private fun write(level: String, tag: String, msg: String) {
        runCatching {
            if (file.length() >= MAX_BYTES) file.renameTo(File(dir, "$FILE.1"))
            file.appendText("${LocalDateTime.now().format(TIME)} $level $tag: ${Redact.line(msg)}\n")
        }
    }

    companion object {
        const val FILE = "app.log"
        const val CORE = "core.log" // xscore.LogFile
        private const val MAX_BYTES = 256 * 1024L
        private val TIME = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss")
    }
}
