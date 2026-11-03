package com.universalclipboard.ui.history

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
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
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.universalclipboard.R
import com.universalclipboard.clipboard.AndroidClipboard
import com.universalclipboard.data.repository.ClipboardRepository
import com.universalclipboard.domain.Direction
import com.universalclipboard.domain.HistoryEntry
import com.universalclipboard.ui.components.appContainer
import com.universalclipboard.ui.components.deliveryLabel
import com.universalclipboard.ui.components.relativeTime
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn

class HistoryViewModel(private val repo: ClipboardRepository, private val clipboard: AndroidClipboard) : ViewModel() {
    val entries = repo.history
    val settings = repo.settings
    val names = repo.devices.combine(repo.history) { d, _ -> d.associate { it.device.id to it.device.name } }
        .stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), emptyMap())

    fun copy(e: HistoryEntry) = clipboard.copy(e.text)
    fun delete(e: HistoryEntry) = repo.deleteHistoryEntry(e.eventId)
    fun clear() = repo.clearHistory()
}

@Composable
fun HistoryScreen(modifier: Modifier = Modifier) {
    val container = appContainer()
    val vm: HistoryViewModel = viewModel(factory = viewModelFactory { initializer { HistoryViewModel(container.repository, container.clipboard) } })
    val entries by vm.entries.collectAsStateWithLifecycle()
    val settings by vm.settings.collectAsStateWithLifecycle()
    val names by vm.names.collectAsStateWithLifecycle()
    var confirmClear by remember { mutableStateOf(false) }
    var copied by remember { mutableStateOf<String?>(null) }

    Column(modifier.fillMaxSize()) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(
                if (!settings.historyEnabled) stringResource(R.string.history_disabled)
                else if (settings.historyPersist) stringResource(R.string.history_saved) else stringResource(R.string.history_memory),
                style = MaterialTheme.typography.bodySmall,
                modifier = Modifier.weight(1f),
            )
            TextButton(onClick = { confirmClear = true }, enabled = entries.isNotEmpty()) { Text(stringResource(R.string.action_clear_history)) }
        }
        if (entries.isEmpty()) {
            Text(stringResource(R.string.history_empty), Modifier.padding(24.dp), style = MaterialTheme.typography.bodyMedium)
        }
        LazyColumn(contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            items(entries, key = { it.eventId }) { e ->
                HistoryCard(e, names, copied == e.eventId, onCopy = { vm.copy(e); copied = e.eventId }, onDelete = { vm.delete(e) })
            }
        }
    }

    if (confirmClear) {
        AlertDialog(
            onDismissRequest = { confirmClear = false },
            title = { Text(stringResource(R.string.clear_history_title)) },
            text = { Text(stringResource(R.string.clear_history_body)) },
            confirmButton = { TextButton(onClick = { vm.clear(); confirmClear = false }) { Text(stringResource(R.string.action_clear_history)) } },
            dismissButton = { TextButton(onClick = { confirmClear = false }) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
}

@Composable
private fun HistoryCard(e: HistoryEntry, names: Map<String, String>, copied: Boolean, onCopy: () -> Unit, onDelete: () -> Unit) {
    val incoming = e.direction == Direction.INCOMING
    val peer = if (incoming) names[e.fromDeviceId] ?: e.fromDeviceId?.take(8) ?: "?" else null
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    if (incoming) stringResource(R.string.history_from, peer!!) else stringResource(R.string.history_sent),
                    style = MaterialTheme.typography.labelLarge,
                    modifier = Modifier.weight(1f),
                )
                Text(relativeTime(e.recordedAt), style = MaterialTheme.typography.labelSmall)
                IconButton(onClick = onDelete) { Icon(Icons.Default.Delete, stringResource(R.string.action_delete)) }
            }
            Text(e.text, maxLines = 4, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodyMedium)
            if (!incoming && e.deliveries.isNotEmpty()) {
                val parts = e.deliveries.map { (id, s) -> "${names[id] ?: id.take(8)}: ${deliveryLabel(s)}" }
                Text(
                    parts.joinToString(),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            TextButton(onClick = onCopy) { Text(if (copied) stringResource(R.string.copied) else stringResource(R.string.action_copy)) }
        }
    }
}
