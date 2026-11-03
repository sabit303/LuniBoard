package com.universalclipboard.ui.home

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Clear
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.universalclipboard.R
import com.universalclipboard.data.repository.DeviceWithState
import com.universalclipboard.domain.AppSettings
import com.universalclipboard.ui.components.DeviceSelector
import com.universalclipboard.ui.components.SendResultList
import com.universalclipboard.ui.components.appContainer

@Composable
fun HomeScreen(onPair: () -> Unit, modifier: Modifier = Modifier) {
    val container = appContainer()
    val vm: HomeViewModel = viewModel(factory = viewModelFactory { initializer { HomeViewModel(container.repository, container.clipboard) } })
    val ui by vm.ui.collectAsStateWithLifecycle()
    val devices by vm.devices.collectAsStateWithLifecycle()
    val settings by vm.settings.collectAsStateWithLifecycle()
    val emptyMsg = stringResource(R.string.toast_clipboard_empty)
    val sensitiveMsg = stringResource(R.string.paste_sensitive_warning)

    Column(
        modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        StatusCard(devices, settings, onPair = onPair, onEnable = vm::enableSync, onRetry = vm::retry)

        OutlinedTextField(
            value = ui.text,
            onValueChange = vm::setText,
            modifier = Modifier.fillMaxWidth().heightIn(min = 140.dp),
            label = { Text(stringResource(R.string.home_text_label)) },
            placeholder = { Text(stringResource(R.string.home_text_placeholder)) },
            enabled = !ui.sending,
        )
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { vm.paste(emptyMsg, sensitiveMsg) }, enabled = !ui.sending) {
                Icon(painterResource(R.drawable.ic_paste), null, Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.action_paste))
            }
            TextButton(onClick = { vm.setText("") }, enabled = ui.text.isNotEmpty() && !ui.sending) {
                Icon(Icons.Default.Clear, null, Modifier.size(18.dp))
                Spacer(Modifier.width(4.dp))
                Text(stringResource(R.string.action_clear))
            }
        }

        if (devices.isNotEmpty()) {
            DeviceSelector(devices, vm.selection(devices), enabled = !ui.sending, onChange = vm::setSelection)
        }

        Button(
            onClick = vm::send,
            enabled = !ui.sending && ui.text.isNotEmpty() && devices.isNotEmpty() && vm.selection(devices).isNotEmpty() && settings.syncEnabled,
            modifier = Modifier.fillMaxWidth(),
        ) {
            if (ui.sending) {
                CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
            } else {
                Icon(Icons.AutoMirrored.Filled.Send, null, Modifier.size(18.dp))
            }
            Spacer(Modifier.width(8.dp))
            Text(stringResource(R.string.action_send))
        }

        ui.result?.let { SendResultList(it, devices) }
        ui.message?.let { Text(it, color = MaterialTheme.colorScheme.error) }

        Text(
            stringResource(R.string.home_android_hint),
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

@Composable
private fun StatusCard(devices: List<DeviceWithState>, settings: AppSettings, onPair: () -> Unit, onEnable: () -> Unit, onRetry: () -> Unit) {
    val connected = devices.count { it.connected }
    val (title, body) = when {
        !settings.syncEnabled -> stringResource(R.string.status_sync_off_title) to stringResource(R.string.status_sync_off_body)
        devices.isEmpty() -> stringResource(R.string.status_no_devices_title) to stringResource(R.string.status_no_devices_body)
        connected == 0 -> stringResource(R.string.status_none_connected_title) to stringResource(R.string.status_none_connected_body)
        else -> stringResource(R.string.status_connected_title, connected, devices.size) to
            (if (settings.backgroundConnection) stringResource(R.string.status_background_on) else stringResource(R.string.status_background_off))
    }
    Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.secondaryContainer)) {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(title, style = MaterialTheme.typography.titleMedium)
            Text(body, style = MaterialTheme.typography.bodyMedium)
            Row(verticalAlignment = Alignment.CenterVertically) {
                when {
                    !settings.syncEnabled -> TextButton(onClick = onEnable) { Text(stringResource(R.string.action_enable_sync)) }
                    devices.isEmpty() -> TextButton(onClick = onPair) { Text(stringResource(R.string.action_pair_computer)) }
                    connected < devices.size -> TextButton(onClick = onRetry) { Text(stringResource(R.string.action_retry)) }
                }
            }
        }
    }
}
