package com.universalclipboard.ui.home

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.universalclipboard.clipboard.AndroidClipboard
import com.universalclipboard.data.repository.ClipboardRepository
import com.universalclipboard.data.repository.DeviceWithState
import com.universalclipboard.domain.AppSettings
import com.universalclipboard.domain.SendResult
import com.universalclipboard.domain.Source
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class HomeUiState(
    val text: String = "",
    /** null = default selection (all devices with sending enabled). */
    val selected: Set<String>? = null,
    val sending: Boolean = false,
    val result: SendResult? = null,
    val message: String? = null,
)

class HomeViewModel(private val repo: ClipboardRepository, private val clipboard: AndroidClipboard) : ViewModel() {
    private val _ui = MutableStateFlow(HomeUiState())
    val ui: StateFlow<HomeUiState> = _ui.asStateFlow()

    val devices: StateFlow<List<DeviceWithState>> = repo.devices.stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), emptyList())
    val settings: StateFlow<AppSettings> = repo.settings

    fun selection(devices: List<DeviceWithState>): Set<String> =
        _ui.value.selected ?: devices.filter { it.device.permissions.send }.map { it.device.id }.toSet()

    fun setText(t: String) = _ui.update { it.copy(text = t, result = null, message = null) }

    fun setSelection(s: Set<String>) = _ui.update { it.copy(selected = s) }

    /** Reads the clipboard; only works because the app has focus while the user taps Paste. */
    fun paste(emptyMessage: String, sensitiveMessage: String) {
        val clip = clipboard.readText()
        _ui.update {
            when {
                clip == null -> it.copy(message = emptyMessage)
                else -> it.copy(text = clip.text, result = null, message = if (clip.sensitive) sensitiveMessage else null)
            }
        }
    }

    fun send() {
        val state = _ui.value
        if (state.sending || state.text.isEmpty()) return
        val targets = selection(devices.value)
        _ui.update { it.copy(sending = true, result = null, message = null) }
        viewModelScope.launch {
            try {
                repo.awaitConnected(3_000)
                val r = repo.send(state.text, targets, Source.MANUAL)
                _ui.update { it.copy(result = r) }
            } catch (e: Exception) {
                _ui.update { it.copy(message = e.message) }
            } finally {
                _ui.update { it.copy(sending = false) }
            }
        }
    }

    fun retry() = repo.retryConnections()

    fun enableSync() = repo.updateSettings { it.copy(syncEnabled = true) }
}
