package com.zyndata.crossstream.relay.model

import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

/** What the relay service is doing. */
enum class Phase { OFF, SEARCHING, CONNECTING, RELAYING, PAUSED }

/** What the UI shows; written by RelayService. */
data class RelayUiState(
    val phase: Phase = Phase.OFF,
    /** Hub of the current session ("" while searching, paused or off). */
    val hubId: String = "",
    /** control.Leaving reason while paused: limit, battery, no_data. */
    val pausedReason: String = "",
    val stats: RelayStats = RelayStats(),
    /** Wi-Fi is off: the relay waits for it (no hub can be joined). */
    val wifiOff: Boolean = false,
    /** Saved hubs seen in the last Wi-Fi scan; null = the phone cannot scan (range unknown). */
    val inRange: Set<String>? = null,
    /** Bytes relayed since the toggle was switched on. */
    val sessionShared: Long = 0,
    val lastStop: String = "",
) {
    val serviceRunning: Boolean get() = phase != Phase.OFF
}

/** Process-wide relay state shared by the service and the UI. */
object RelayStateHolder {
    private val state = MutableStateFlow(RelayUiState())
    val flow: StateFlow<RelayUiState> = state.asStateFlow()

    fun update(f: (RelayUiState) -> RelayUiState) = state.update(f)

    private val wake = MutableSharedFlow<Unit>(extraBufferCapacity = 1, onBufferOverflow = BufferOverflow.DROP_OLDEST)

    /** Asks a searching relay to scan now (the screen saw a saved hub in range). */
    val wakeups: SharedFlow<Unit> = wake

    fun wakeSearch() {
        wake.tryEmit(Unit)
    }

    private val switch = MutableSharedFlow<Unit>(extraBufferCapacity = 1, onBufferOverflow = BufferOverflow.DROP_OLDEST)

    /** Asks the relay to leave its hub and join the chosen one now ("Switch now"). */
    val switchRequests: SharedFlow<Unit> = switch

    fun requestSwitch() {
        switch.tryEmit(Unit)
    }
}
