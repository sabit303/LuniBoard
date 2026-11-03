package com.universalclipboard

import android.Manifest
import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import com.universalclipboard.navigation.AppNavigation
import com.universalclipboard.service.Notifications
import com.universalclipboard.ui.theme.UniversalClipboardTheme

class MainActivity : ComponentActivity() {
    /** A uclip://pair link opened from the camera app or a browser. */
    private var pairingLink by mutableStateOf<String?>(null)

    private val notificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (savedInstanceState == null) pairingLink = linkFrom(intent)
        setContent {
            UniversalClipboardTheme {
                AppNavigation(pairingLink, onPairingLinkConsumed = { pairingLink = null })
            }
        }
        val container = (application as UniversalClipboardApp).container
        if (savedInstanceState == null && Build.VERSION.SDK_INT >= 33 && container.settingsStore.current.notifyOnReceive && !Notifications.canPost(this)) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        linkFrom(intent)?.let { pairingLink = it }
    }

    private fun linkFrom(intent: Intent?): String? =
        intent?.takeIf { it.action == Intent.ACTION_VIEW }?.data?.takeIf { it.scheme == "uclip" }?.toString()
}
