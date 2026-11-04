package com.luniboard.ui.components

import android.text.format.DateUtils
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.luniboard.R
import com.luniboard.LuniBoardApp
import com.luniboard.data.repository.DeviceWithState
import com.luniboard.di.AppContainer
import com.luniboard.domain.ConnectionStatus
import com.luniboard.domain.SendResult
import com.luniboard.ui.theme.ConnectedGreen

@Composable
fun appContainer(): AppContainer = (LocalContext.current.applicationContext as LuniBoardApp).container

@Composable
fun StatusDot(status: ConnectionStatus) {
    val color = when (status) {
        ConnectionStatus.CONNECTED -> ConnectedGreen
        ConnectionStatus.CONNECTING -> MaterialTheme.colorScheme.tertiary
        ConnectionStatus.UNAUTHORIZED -> MaterialTheme.colorScheme.error
        else -> MaterialTheme.colorScheme.outline
    }
    Box(Modifier.size(10.dp).background(color, CircleShape))
}

@Composable
fun statusLabel(status: ConnectionStatus): String = stringResource(
    when (status) {
        ConnectionStatus.CONNECTED -> R.string.status_connected
        ConnectionStatus.CONNECTING -> R.string.status_connecting
        ConnectionStatus.OFFLINE -> R.string.status_offline
        ConnectionStatus.UNAUTHORIZED -> R.string.status_unauthorized
        ConnectionStatus.IDLE -> R.string.status_idle
    },
)

fun platformLabel(platform: String) = when (platform) {
    "linux" -> "Linux"
    "windows" -> "Windows"
    "macos" -> "macOS"
    "android" -> "Android"
    "ios" -> "iOS"
    else -> "Other"
}

fun relativeTime(millis: Long): String =
    if (millis <= 0) "never" else DateUtils.getRelativeTimeSpanString(millis, System.currentTimeMillis(), DateUtils.MINUTE_IN_MILLIS).toString()

/** Destination picker: one chip per paired device; offline devices can still be selected. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun DeviceSelector(devices: List<DeviceWithState>, selected: Set<String>, enabled: Boolean, onChange: (Set<String>) -> Unit) {
    Column {
        Text(stringResource(R.string.send_to), style = MaterialTheme.typography.labelLarge)
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            devices.forEach { d ->
                val id = d.device.id
                val isSelected = id in selected
                FilterChip(
                    selected = isSelected,
                    enabled = enabled && d.device.permissions.send,
                    onClick = { onChange(if (isSelected) selected - id else selected + id) },
                    label = {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            StatusDot(d.state.status)
                            Text(d.device.name)
                        }
                    },
                    leadingIcon = if (isSelected) ({ Icon(Icons.Default.Check, null, Modifier.size(16.dp)) }) else null,
                )
            }
        }
    }
}

@Composable
fun SendResultList(result: SendResult, devices: List<DeviceWithState>) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        if (result.results.isEmpty()) {
            Text(stringResource(R.string.result_no_targets), color = MaterialTheme.colorScheme.error)
        }
        result.results.forEach { (id, outcome) ->
            val name = devices.firstOrNull { it.device.id == id }?.device?.name ?: id.take(8)
            val color = if (outcome.delivered) ConnectedGreen else if (outcome.status == "sent") Color.Unspecified else MaterialTheme.colorScheme.error
            Text("$name: ${deliveryLabel(outcome.status)}${outcome.reason?.let { " ($it)" } ?: ""}", color = color)
        }
    }
}

@Composable
fun deliveryLabel(status: String): String = when (status) {
    "applied" -> stringResource(R.string.delivery_applied)
    "duplicate" -> stringResource(R.string.delivery_duplicate)
    "sent" -> stringResource(R.string.delivery_sent)
    "offline" -> stringResource(R.string.delivery_offline)
    "ignored" -> stringResource(R.string.delivery_ignored)
    "stale" -> stringResource(R.string.delivery_stale)
    "expired" -> stringResource(R.string.delivery_expired)
    "failed" -> stringResource(R.string.delivery_failed)
    else -> status.replace('_', ' ')
}
