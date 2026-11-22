package com.zyndata.crossstream.relay.data

import com.zyndata.crossstream.relay.logic.RelaySettings
import com.zyndata.crossstream.relay.logic.Usage
import java.time.ZoneId
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update

/**
 * The data counter in memory (one owner for the service and the UI); the
 * service adds relayed bytes every tick and flushes to DataStore every 30 s
 * and on stop (docs/decisions.md, phase 08).
 */
class UsageMeter(private val repo: Repository) {
    private val state = MutableStateFlow(Usage())
    private val loaded = CompletableDeferred<Unit>()
    val flow: StateFlow<Usage> = state.asStateFlow()

    suspend fun load() {
        state.value = repo.usage.first()
        loaded.complete(Unit)
    }

    suspend fun awaitLoaded() = loaded.await()

    fun add(delta: Long, s: RelaySettings, sessionStart: Long, nowMs: Long = System.currentTimeMillis()) =
        state.update { it.add(delta, s, nowMs, sessionStart, ZoneId.systemDefault()) }

    /** Rolls into a new period if the current one ended; returns the current usage. */
    fun roll(s: RelaySettings, sessionStart: Long, nowMs: Long = System.currentTimeMillis()): Usage {
        state.update { it.roll(s, nowMs, sessionStart, ZoneId.systemDefault()) }
        return state.value
    }

    suspend fun reset(nowMs: Long = System.currentTimeMillis()) {
        state.update { it.reset(nowMs) }
        flush()
    }

    suspend fun flush() {
        val u = state.value
        repo.updateUsage { u }
    }
}
