package com.luniboard.ui.home

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
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.luniboard.R
import com.luniboard.data.repository.DeviceWithState
import com.luniboard.domain.AppSettings
import com.luniboard.domain.ConnectionStatus
import com.luniboard.ui.components.DeviceSelector
import com.luniboard.ui.components.SendResultList
import com.luniboard.ui.components.StatusDot
import com.luniboard.ui.components.appContainer

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
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        StatusLine(devices, settings, onPair = onPair, onEnable = vm::enableSync, onRetry = vm::retry)

        OutlinedTextField(
            value = ui.text,
            onValueChange = vm::setText,
            modifier = Modifier.fillMaxWidth().heightIn(min = 140.dp),
            placeholder = { Text(stringResource(R.string.home_text_placeholder)) },
            enabled = !ui.sending,
            trailingIcon = if (ui.text.isNotEmpty() && !ui.sending) {
                { IconButton(onClick = { vm.setText("") }) { Icon(Icons.Default.Clear, stringResource(R.string.action_clear)) } }
            } else {
                null
            },
        )

        if (devices.size > 1) {
            DeviceSelector(devices, vm.selection(devices), enabled = !ui.sending, onChange = vm::setSelection)
        }

        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            OutlinedButton(onClick = { vm.paste(emptyMsg, sensitiveMsg) }, enabled = !ui.sending, modifier = Modifier.weight(1f)) {
                Icon(painterResource(R.drawable.ic_paste), null, Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.action_paste))
            }
            Button(
                onClick = vm::send,
                enabled = !ui.sending && ui.text.isNotEmpty() && devices.isNotEmpty() && vm.selection(devices).isNotEmpty() && settings.syncEnabled,
                modifier = Modifier.weight(1f),
            ) {
                if (ui.sending) {
                    CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                } else {
                    Icon(Icons.AutoMirrored.Filled.Send, null, Modifier.size(18.dp))
                }
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.action_send))
            }
        }

        ui.result?.let { SendResultList(it, devices) }
        ui.message?.let { Text(it, color = MaterialTheme.colorScheme.error) }
    }
}

/** One line: who we are connected to, or what is wrong, with a single action. */
@Composable
private fun StatusLine(devices: List<DeviceWithState>, settings: AppSettings, onPair: () -> Unit, onEnable: () -> Unit, onRetry: () -> Unit) {
    val connected = devices.filter { it.connected }
    val text = when {
        !settings.syncEnabled -> stringResource(R.string.status_line_sync_off)
        devices.isEmpty() -> stringResource(R.string.status_line_no_devices)
        connected.isEmpty() -> stringResource(R.string.status_line_offline)
        else -> stringResource(R.string.status_line_connected, connected.joinToString { it.device.name })
    }
    val status = when {
        !settings.syncEnabled || devices.isEmpty() -> ConnectionStatus.IDLE
        connected.isEmpty() -> ConnectionStatus.OFFLINE
        else -> ConnectionStatus.CONNECTED
    }
    Row(Modifier.fillMaxWidth().heightIn(min = 40.dp), verticalAlignment = Alignment.CenterVertically) {
        StatusDot(status)
        Spacer(Modifier.width(10.dp))
        Text(text, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        when {
            !settings.syncEnabled -> TextButton(onClick = onEnable) { Text(stringResource(R.string.action_turn_on)) }
            devices.isEmpty() -> TextButton(onClick = onPair) { Text(stringResource(R.string.action_pair)) }
            connected.isEmpty() -> TextButton(onClick = onRetry) { Text(stringResource(R.string.action_retry)) }
        }
    }
}
