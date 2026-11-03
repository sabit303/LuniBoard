package com.universalclipboard.ui.pairing

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import com.universalclipboard.R
import com.universalclipboard.data.network.DiscoveredDevice
import com.universalclipboard.data.network.PAIRING_CODE_LENGTH
import com.universalclipboard.data.network.normalizePairingCode
import com.universalclipboard.ui.components.appContainer
import com.universalclipboard.ui.components.platformLabel
import androidx.activity.compose.rememberLauncherForActivityResult

@Composable
fun PairingScreen(initialLink: String?, onDone: () -> Unit, modifier: Modifier = Modifier) {
    val container = appContainer()
    val vm: PairingViewModel = viewModel(factory = viewModelFactory { initializer { PairingViewModel(container.repository) } })
    val state by vm.state.collectAsStateWithLifecycle()
    val nearby by vm.nearby.collectAsStateWithLifecycle()
    var link by remember { mutableStateOf("") }
    var codeFor by remember { mutableStateOf<DiscoveredDevice?>(null) }

    LaunchedEffect(initialLink) { if (!initialLink.isNullOrBlank()) vm.onLink(initialLink) }

    val scanner = rememberLauncherForActivityResult(ScanContract()) { result -> result.contents?.let(vm::onLink) }

    Column(modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
        when (val s = state) {
            is PairingState.Waiting -> WaitingCard(s, onCancel = vm::cancel)
            is PairingState.Success -> {
                Card {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(stringResource(R.string.pair_success, s.device.name), style = MaterialTheme.typography.titleMedium)
                        Text(stringResource(R.string.pair_success_body))
                        Button(onClick = onDone) { Text(stringResource(R.string.action_done)) }
                    }
                }
            }
            is PairingState.Confirm -> {
                Card {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(stringResource(R.string.pair_confirm, s.invitation.device.name), style = MaterialTheme.typography.titleMedium)
                        Text("${platformLabel(s.invitation.device.platform)} · ${s.invitation.addresses.joinToString()}", style = MaterialTheme.typography.bodySmall)
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            Button(onClick = { vm.start(s.invitation) }) { Text(stringResource(R.string.action_pair)) }
                            TextButton(onClick = vm::reset) { Text(stringResource(R.string.action_cancel)) }
                        }
                    }
                }
            }
            is PairingState.Failed -> {
                Card {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(stringResource(R.string.pair_failed), style = MaterialTheme.typography.titleMedium, color = MaterialTheme.colorScheme.error)
                        Text(s.message)
                        TextButton(onClick = vm::reset) { Text(stringResource(R.string.action_try_again)) }
                    }
                }
            }
            PairingState.Idle -> {
                Text(stringResource(R.string.pair_intro), style = MaterialTheme.typography.bodyMedium)
                Button(
                    onClick = {
                        scanner.launch(
                            ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE).setBeepEnabled(false)
                                .setOrientationLocked(false).setPrompt(""),
                        )
                    },
                    modifier = Modifier.fillMaxWidth(),
                ) { Text(stringResource(R.string.pair_scan_qr)) }

                HorizontalDivider()
                Text(stringResource(R.string.pair_nearby), style = MaterialTheme.typography.titleSmall)
                if (nearby.isEmpty()) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        CircularProgressIndicator(Modifier.padding(4.dp), strokeWidth = 2.dp)
                        Text(stringResource(R.string.pair_nearby_searching), style = MaterialTheme.typography.bodySmall)
                    }
                } else {
                    nearby.forEach { d ->
                        Card(Modifier.fillMaxWidth().clickable { codeFor = d }) {
                            Column(Modifier.padding(12.dp)) {
                                Text(d.name, style = MaterialTheme.typography.titleSmall)
                                Text("${platformLabel(d.platform)} · ${d.addresses.first()}", style = MaterialTheme.typography.bodySmall)
                            }
                        }
                    }
                }

                HorizontalDivider()
                Text(stringResource(R.string.pair_link), style = MaterialTheme.typography.titleSmall)
                OutlinedTextField(
                    value = link,
                    onValueChange = { link = it },
                    modifier = Modifier.fillMaxWidth(),
                    placeholder = { Text("uclip://pair?…") },
                    singleLine = true,
                )
                OutlinedButton(onClick = { vm.onLink(link) }, enabled = link.isNotBlank()) { Text(stringResource(R.string.action_continue)) }
            }
        }
    }

    codeFor?.let { d ->
        var code by remember(d) { mutableStateOf("") }
        AlertDialog(
            onDismissRequest = { codeFor = null },
            title = { Text(stringResource(R.string.pair_enter_code, d.name)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(stringResource(R.string.pair_enter_code_body))
                    OutlinedTextField(
                        value = code,
                        onValueChange = { code = it.take(12) },
                        singleLine = true,
                        placeholder = { Text("XXXX-XXXX") },
                        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Characters, autoCorrectEnabled = false),
                    )
                }
            },
            confirmButton = {
                TextButton(
                    enabled = normalizePairingCode(code).length == PAIRING_CODE_LENGTH,
                    onClick = { vm.pairNearby(d, code); codeFor = null },
                ) { Text(stringResource(R.string.action_pair)) }
            },
            dismissButton = { TextButton(onClick = { codeFor = null }) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
}

@Composable
private fun WaitingCard(s: PairingState.Waiting, onCancel: () -> Unit) {
    Card {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(stringResource(R.string.pair_waiting, s.invitation.device.name), style = MaterialTheme.typography.titleMedium)
            Text(stringResource(R.string.pair_verification_label))
            Text(s.verificationCode, fontSize = 36.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace)
            Text(stringResource(R.string.pair_verification_body), style = MaterialTheme.typography.bodyMedium)
            CircularProgressIndicator()
            TextButton(onClick = onCancel) { Text(stringResource(R.string.action_cancel)) }
        }
    }
}
