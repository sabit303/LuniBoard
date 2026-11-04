package com.luniboard.ui.devices

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.luniboard.R
import com.luniboard.data.repository.ClipboardRepository
import com.luniboard.data.repository.DeviceWithState
import com.luniboard.domain.ConnectionStatus
import com.luniboard.domain.Permissions
import com.luniboard.ui.components.StatusDot
import com.luniboard.ui.components.appContainer
import com.luniboard.ui.components.platformLabel
import com.luniboard.ui.components.relativeTime
import com.luniboard.ui.components.statusLabel
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.stateIn

class DevicesViewModel(private val repo: ClipboardRepository) : ViewModel() {
    val devices = repo.devices.stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), emptyList())
    fun setPermissions(id: String, p: Permissions) = repo.setPermissions(id, p)
    fun unpair(id: String) = repo.unpair(id)
    fun retry() = repo.retryConnections()
}

@Composable
fun DevicesScreen(onPair: () -> Unit, modifier: Modifier = Modifier) {
    val container = appContainer()
    val vm: DevicesViewModel = viewModel(factory = viewModelFactory { initializer { DevicesViewModel(container.repository) } })
    val devices by vm.devices.collectAsStateWithLifecycle()
    var confirmRemove by remember { mutableStateOf<DeviceWithState?>(null) }

    Column(modifier.fillMaxSize()) {
        if (devices.isEmpty()) {
            Column(Modifier.weight(1f).fillMaxWidth().padding(24.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
                Text(stringResource(R.string.devices_empty_title), style = MaterialTheme.typography.titleMedium)
                Spacer(Modifier.padding(4.dp))
                Text(stringResource(R.string.devices_empty_body), style = MaterialTheme.typography.bodyMedium)
            }
        } else {
            LazyColumn(Modifier.weight(1f), contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                items(devices, key = { it.device.id }) { d ->
                    DeviceCard(d, onPermissions = { vm.setPermissions(d.device.id, it) }, onRemove = { confirmRemove = d }, onRetry = vm::retry)
                }
            }
        }
        Row(Modifier.fillMaxWidth().padding(16.dp), horizontalArrangement = Arrangement.End) {
            ExtendedFloatingActionButton(onClick = onPair, icon = { Icon(Icons.Default.Add, null) }, text = { Text(stringResource(R.string.action_pair_computer)) })
        }
    }

    confirmRemove?.let { d ->
        AlertDialog(
            onDismissRequest = { confirmRemove = null },
            title = { Text(stringResource(R.string.unpair_title, d.device.name)) },
            text = { Text(stringResource(R.string.unpair_body, d.device.name)) },
            confirmButton = {
                TextButton(onClick = { vm.unpair(d.device.id); confirmRemove = null }) { Text(stringResource(R.string.action_remove)) }
            },
            dismissButton = { TextButton(onClick = { confirmRemove = null }) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
}

@Composable
private fun DeviceCard(d: DeviceWithState, onPermissions: (Permissions) -> Unit, onRemove: () -> Unit, onRetry: () -> Unit) {
    val p = d.device.permissions
    Card {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(d.device.name, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                Text(platformLabel(d.device.platform), style = MaterialTheme.typography.labelMedium)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(d.state.status)
                Spacer(Modifier.width(8.dp))
                Text(statusLabel(d.state.status), style = MaterialTheme.typography.bodyMedium)
                if (d.state.status == ConnectionStatus.CONNECTED && d.state.address != null) {
                    Text("  ·  ${d.state.address}", style = MaterialTheme.typography.bodySmall)
                }
            }
            if (d.state.status != ConnectionStatus.CONNECTED) {
                d.state.detail?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                Text(stringResource(R.string.last_seen, relativeTime(d.device.lastSeen)), style = MaterialTheme.typography.bodySmall)
            }
            PermissionRow(stringResource(R.string.permission_send), p.send) { onPermissions(p.copy(send = it)) }
            PermissionRow(stringResource(R.string.permission_receive), p.receive) { onPermissions(p.copy(receive = it)) }
            Row {
                if (d.state.status == ConnectionStatus.OFFLINE || d.state.status == ConnectionStatus.UNAUTHORIZED) {
                    TextButton(onClick = onRetry) { Text(stringResource(R.string.action_retry)) }
                }
                Spacer(Modifier.weight(1f))
                TextButton(onClick = onRemove) { Text(stringResource(R.string.action_remove), color = MaterialTheme.colorScheme.error) }
            }
        }
    }
}

@Composable
private fun PermissionRow(label: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(label, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
        Switch(checked = checked, onCheckedChange = onChange)
    }
}
