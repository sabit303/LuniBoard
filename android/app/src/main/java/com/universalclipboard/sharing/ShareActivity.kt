package com.universalclipboard.sharing

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.universalclipboard.MainActivity
import com.universalclipboard.R
import com.universalclipboard.UniversalClipboardApp
import com.universalclipboard.domain.SendResult
import com.universalclipboard.domain.Source
import com.universalclipboard.ui.components.DeviceSelector
import com.universalclipboard.ui.components.SendResultList
import com.universalclipboard.ui.theme.UniversalClipboardTheme
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Receives text and links from the Android share sheet (ACTION_SEND) and from
 * the text-selection menu (ACTION_PROCESS_TEXT) and sends them to computers.
 */
class ShareActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val text = sharedText(intent)
        setContent {
            UniversalClipboardTheme {
                ShareDialog(text, onDone = ::finish, onOpenApp = {
                    startActivity(Intent(this, MainActivity::class.java))
                    finish()
                })
            }
        }
    }

    private fun sharedText(intent: Intent): String = when (intent.action) {
        Intent.ACTION_SEND -> {
            val subject = intent.getStringExtra(Intent.EXTRA_SUBJECT)
            val body = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString().orEmpty()
            // Some apps put the title in EXTRA_SUBJECT and the link in EXTRA_TEXT; send the link.
            if (body.isNotEmpty()) body else subject.orEmpty()
        }
        Intent.ACTION_PROCESS_TEXT -> intent.getCharSequenceExtra(Intent.EXTRA_PROCESS_TEXT)?.toString().orEmpty()
        else -> ""
    }
}

@Composable
private fun ShareDialog(initial: String, onDone: () -> Unit, onOpenApp: () -> Unit) {
    val app = androidx.compose.ui.platform.LocalContext.current.applicationContext as UniversalClipboardApp
    val repo = app.container.repository
    val devices by repo.devices.collectAsStateWithLifecycle(emptyList())
    val scope = rememberCoroutineScope()
    var text by remember { mutableStateOf(initial) }
    var selected by remember { mutableStateOf<Set<String>?>(null) }
    var sending by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf<SendResult?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    val sendable = devices.filter { it.device.permissions.send }
    val effectiveSelection = selected ?: sendable.map { it.device.id }.toSet()

    AlertDialog(
        onDismissRequest = onDone,
        title = { Text(stringResource(R.string.share_title)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                if (devices.isEmpty()) {
                    Text(stringResource(R.string.share_no_devices))
                    return@Column
                }
                OutlinedTextField(
                    value = text,
                    onValueChange = { text = it },
                    modifier = Modifier.fillMaxWidth().heightIn(max = 180.dp),
                    enabled = !sending && result == null,
                )
                Spacer(Modifier.height(12.dp))
                DeviceSelector(devices, effectiveSelection, enabled = !sending && result == null) { selected = it }
                result?.let {
                    Spacer(Modifier.height(12.dp))
                    SendResultList(it, devices)
                }
                error?.let {
                    Spacer(Modifier.height(12.dp))
                    Text(it, color = MaterialTheme.colorScheme.error)
                }
                if (sending) {
                    Spacer(Modifier.height(12.dp))
                    CircularProgressIndicator()
                }
            }
        },
        confirmButton = {
            when {
                devices.isEmpty() -> TextButton(onClick = onOpenApp) { Text(stringResource(R.string.action_pair_computer)) }
                result != null -> TextButton(onClick = onDone) { Text(stringResource(R.string.action_done)) }
                else -> TextButton(
                    enabled = !sending && text.isNotEmpty() && effectiveSelection.isNotEmpty(),
                    onClick = {
                        sending = true
                        error = null
                        scope.launch {
                            try {
                                repo.awaitConnected(5_000)
                                val r = repo.send(text, effectiveSelection, Source.SHARE)
                                result = r
                                if (r.results.isNotEmpty() && r.results.values.all { it.delivered }) {
                                    delay(1_200)
                                    onDone()
                                }
                            } catch (e: Exception) {
                                error = e.message
                            } finally {
                                sending = false
                            }
                        }
                    },
                ) { Text(stringResource(R.string.action_send)) }
            }
        },
        dismissButton = { TextButton(onClick = onDone) { Text(stringResource(R.string.action_cancel)) } },
    )
}
