package com.zyndata.crossstream.relay

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Surface
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import com.zyndata.crossstream.relay.logic.ThemeMode
import com.zyndata.crossstream.relay.ui.RelayApp
import com.zyndata.crossstream.relay.ui.RelayTheme
import kotlinx.coroutines.launch

/** Hosts the Compose UI only; relay logic lives in RelayService and the Go core. */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val app = RelayApplication.of(this)
        val repo = app.repo
        // The app's screen opened (not a rotation): the one automatic update check.
        if (savedInstanceState == null) app.scope.launch { app.updates.checkAtStart() }
        setContent {
            val theme by repo.theme.collectAsState(initial = ThemeMode.SYSTEM)
            RelayTheme(theme) {
                Surface(modifier = Modifier.fillMaxSize()) { RelayApp() }
            }
        }
    }
}
