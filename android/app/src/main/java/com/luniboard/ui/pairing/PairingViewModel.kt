package com.luniboard.ui.pairing

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.luniboard.data.network.DiscoveredDevice
import com.luniboard.data.network.FingerprintMismatchException
import com.luniboard.data.network.PairingInvitation
import com.luniboard.data.network.PairingRefusedException
import com.luniboard.data.network.PairingUri
import com.luniboard.data.network.ErrorCodes
import com.luniboard.data.network.findFingerprintMismatch
import com.luniboard.data.repository.ClipboardRepository
import com.luniboard.data.repository.PairingFailedException
import com.luniboard.domain.PairedDevice
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import java.io.IOException

sealed interface PairingState {
    data object Idle : PairingState
    /** A link or QR code was decoded; ask before contacting the computer. */
    data class Confirm(val invitation: PairingInvitation) : PairingState
    data class Waiting(val invitation: PairingInvitation, val verificationCode: String) : PairingState
    data class Success(val device: PairedDevice) : PairingState
    data class Failed(val message: String) : PairingState
}

class PairingViewModel(private val repo: ClipboardRepository) : ViewModel() {
    private val _state = MutableStateFlow<PairingState>(PairingState.Idle)
    val state: StateFlow<PairingState> = _state.asStateFlow()
    val nearby = repo.nearby.stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), emptyList())
    private var job: Job? = null

    fun onLink(raw: String) {
        _state.value = try {
            PairingState.Confirm(PairingUri.parse(raw))
        } catch (e: Exception) {
            PairingState.Failed("This is not a valid pairing code: ${e.message}")
        }
    }

    /**
     * Pairs with a computer found on the network using the code shown on it.
     * The fingerprint comes from the (unauthenticated) mDNS record, so the
     * verification code comparison is what protects this path.
     */
    fun pairNearby(device: DiscoveredDevice, code: String) = start(repo.invitationFor(device, code))

    fun start(invitation: PairingInvitation) {
        job?.cancel()
        _state.value = PairingState.Waiting(invitation, repo.verificationCodeFor(invitation.fingerprint))
        job = viewModelScope.launch {
            _state.value = try {
                PairingState.Success(repo.pair(invitation))
            } catch (e: PairingRefusedException) {
                PairingState.Failed(
                    when (e.code) {
                        ErrorCodes.PAIRING_CLOSED -> "Pairing is not open on ${invitation.device.name}. Run 'luniboard pair' there first."
                        ErrorCodes.INVALID_CODE -> "Wrong pairing code."
                        ErrorCodes.RATE_LIMITED -> "Too many attempts. Wait a moment and try again."
                        else -> e.message ?: "Pairing refused"
                    },
                )
            } catch (e: FingerprintMismatchException) {
                PairingState.Failed(mismatchMessage)
            } catch (e: PairingFailedException) {
                PairingState.Failed(e.message ?: "Pairing failed")
            } catch (e: IOException) {
                if (e.findFingerprintMismatch() != null) PairingState.Failed(mismatchMessage)
                else PairingState.Failed("Could not reach ${invitation.device.name}. Check that both devices are on the same network and that port ${portOf(invitation)} is allowed by the computer's firewall.\n\n${e.message}")
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                PairingState.Failed(e.message ?: "Pairing failed")
            }
        }
    }

    fun cancel() {
        job?.cancel()
        _state.value = PairingState.Idle
    }

    fun reset() {
        _state.value = PairingState.Idle
    }

    private fun portOf(inv: PairingInvitation) = inv.addresses.firstOrNull()?.let { PairingUri.splitHostPort(it)?.second } ?: 47823

    private val mismatchMessage = "The device at this address is not the computer you are pairing with (certificate mismatch). Pairing was stopped for your safety."
}
