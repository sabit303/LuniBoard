package com.universalclipboard.ui.settings

import android.Manifest
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.universalclipboard.BuildConfig
import com.universalclipboard.R
import com.universalclipboard.data.network.Validation
import com.universalclipboard.domain.AppSettings
import com.universalclipboard.service.Notifications
import com.universalclipboard.ui.components.appContainer

@Composable
fun SettingsScreen(modifier: Modifier = Modifier) {
    val repo = appContainer().repository
    val settings by repo.settings.collectAsStateWithLifecycle()
    val context = LocalContext.current
    fun update(fn: (AppSettings) -> AppSettings) = repo.updateSettings(fn)

    var pendingAfterPermission by remember { mutableStateOf<((AppSettings) -> AppSettings)?>(null) }
    val notificationPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) {
        pendingAfterPermission?.let(::update)
        pendingAfterPermission = null
    }
    fun withNotificationPermission(fn: (AppSettings) -> AppSettings) {
        if (Build.VERSION.SDK_INT >= 33 && !Notifications.canPost(context)) {
            pendingAfterPermission = fn
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            update(fn)
        }
    }

    var name by remember(settings.deviceName) { mutableStateOf(settings.deviceName) }

    Column(modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Section(stringResource(R.string.settings_device))
        OutlinedTextField(
            value = name,
            onValueChange = { name = it.take(Validation.MAX_DEVICE_NAME_CHARS) },
            label = { Text(stringResource(R.string.settings_device_name)) },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        if (name.trim() != settings.deviceName && name.isNotBlank()) {
            TextButton(onClick = { update { it.copy(deviceName = name.trim()) } }) { Text(stringResource(R.string.action_save)) }
        }

        Section(stringResource(R.string.settings_sync))
        Toggle(stringResource(R.string.settings_sync_enabled), stringResource(R.string.settings_sync_enabled_body), settings.syncEnabled) { v ->
            update { it.copy(syncEnabled = v) }
        }
        Toggle(stringResource(R.string.settings_receive), stringResource(R.string.settings_receive_body), settings.receiveEnabled) { v ->
            update { it.copy(receiveEnabled = v) }
        }
        Toggle(stringResource(R.string.settings_background), stringResource(R.string.settings_background_body), settings.backgroundConnection) { v ->
            if (v) withNotificationPermission { it.copy(backgroundConnection = true) } else update { it.copy(backgroundConnection = false) }
        }

        Section(stringResource(R.string.settings_notifications))
        Toggle(stringResource(R.string.settings_notify_receive), stringResource(R.string.settings_notify_receive_body), settings.notifyOnReceive) { v ->
            if (v) withNotificationPermission { it.copy(notifyOnReceive = true) } else update { it.copy(notifyOnReceive = false) }
        }

        Section(stringResource(R.string.settings_history))
        Toggle(stringResource(R.string.settings_history_enabled), null, settings.historyEnabled) { v -> update { it.copy(historyEnabled = v) } }
        Toggle(stringResource(R.string.settings_history_persist), stringResource(R.string.settings_history_persist_body), settings.historyPersist, enabled = settings.historyEnabled) { v ->
            update { it.copy(historyPersist = v) }
        }
        OutlinedButton(onClick = repo::clearHistory) { Text(stringResource(R.string.action_clear_history)) }

        Section(stringResource(R.string.settings_limits))
        Text(stringResource(R.string.settings_limits_body), style = MaterialTheme.typography.bodySmall)

        Section(stringResource(R.string.settings_about))
        Text(stringResource(R.string.settings_version, BuildConfig.VERSION_NAME), style = MaterialTheme.typography.bodySmall)
        Text(stringResource(R.string.settings_device_id, repo.deviceId), style = MaterialTheme.typography.bodySmall)
        Text(stringResource(R.string.settings_fingerprint), style = MaterialTheme.typography.bodySmall)
        Text(repo.fingerprint.chunked(4).joinToString(" "), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
    }
}

@Composable
private fun Section(title: String) {
    HorizontalDivider(Modifier.padding(top = 8.dp))
    Text(title, style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary, modifier = Modifier.padding(top = 8.dp))
}

@Composable
private fun Toggle(title: String, body: String?, checked: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.weight(1f).padding(end = 12.dp)) {
            Text(title, style = MaterialTheme.typography.bodyLarge)
            body?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        }
        Switch(checked = checked, onCheckedChange = onChange, enabled = enabled)
    }
}
