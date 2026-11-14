package app.mozillavpnlite

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        MinimalVpnService.startCore(this, on = false)

        setContent {
            MaterialTheme(colorScheme = darkColorScheme(
                primary = Color(0xFF6366F1),
                surface = Color(0xFF0F172A),
                background = Color(0xFF020617)
            )) {
                MinimalVpnApp(activity = this)
            }
        }
    }
}
