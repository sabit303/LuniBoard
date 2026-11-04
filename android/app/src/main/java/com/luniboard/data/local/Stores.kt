package com.luniboard.data.local

import com.luniboard.data.network.ProtocolJson
import com.luniboard.data.network.Validation
import com.luniboard.domain.AppSettings
import com.luniboard.domain.DeviceLookup
import com.luniboard.domain.HistoryEntry
import com.luniboard.domain.HistoryRecorder
import com.luniboard.domain.PairedDevice
import com.luniboard.domain.Permissions
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import java.io.File
import java.io.FileOutputStream

/**
 * Crash-safe JSON file: writes go to a temporary file that is fsynced and
 * renamed. Files live in the app's private storage.
 */
class JsonFile<T>(private val file: File, private val serializer: KSerializer<T>) {
    fun read(): T? {
        if (!file.exists()) return null
        return runCatching { ProtocolJson.decodeFromString(serializer, file.readText()) }.getOrNull()
    }

    fun write(value: T) {
        file.parentFile?.mkdirs()
        val tmp = File(file.parentFile, ".${file.name}.tmp")
        FileOutputStream(tmp).use { out ->
            out.write(ProtocolJson.encodeToString(serializer, value).encodeToByteArray())
            out.fd.sync()
        }
        if (!tmp.renameTo(file)) {
            tmp.delete()
            error("could not replace ${file.name}")
        }
    }

    fun delete() {
        file.delete()
    }
}

/** Trusted computers and their pinned fingerprints. */
class DeviceStore(dir: File) : DeviceLookup {
    @Serializable
    private data class FileFormat(val version: Int = 1, val devices: List<PairedDevice> = emptyList())

    private val file = JsonFile(File(dir, "devices.json"), FileFormat.serializer())
    private val _devices = MutableStateFlow(
        file.read()?.devices.orEmpty().filter { Validation.validId(it.id) && Validation.validFingerprint(it.fingerprint) },
    )
    val devices: StateFlow<List<PairedDevice>> = _devices.asStateFlow()

    override fun get(id: String): PairedDevice? = _devices.value.firstOrNull { it.id == id }

    @Synchronized
    fun add(device: PairedDevice) {
        update { list -> list.filterNot { it.id == device.id || it.fingerprint == device.fingerprint } + device }
    }

    @Synchronized
    fun remove(id: String): PairedDevice? {
        val d = get(id) ?: return null
        update { list -> list.filterNot { it.id == id } }
        return d
    }

    @Synchronized
    fun setPermissions(id: String, permissions: Permissions) = modify(id) { it.copy(permissions = permissions) }

    /** Records a successful connection and moves [address] to the front. */
    @Synchronized
    fun recordSeen(id: String, address: String?, now: Long) = modify(id) { d ->
        val addrs = if (address == null) d.addresses else (listOf(address) + d.addresses).distinct().take(8)
        d.copy(lastSeen = now, addresses = addrs)
    }

    @Synchronized
    fun setInfo(id: String, name: String, platform: String, appVersion: String?) = modify(id) {
        it.copy(name = name, platform = platform, appVersion = appVersion)
    }

    private fun modify(id: String, fn: (PairedDevice) -> PairedDevice) {
        val current = get(id) ?: return
        val next = fn(current)
        if (next != current) update { list -> list.map { if (it.id == id) next else it } }
    }

    private fun update(fn: (List<PairedDevice>) -> List<PairedDevice>) {
        val next = fn(_devices.value).sortedBy { it.name.lowercase() }
        file.write(FileFormat(devices = next))
        _devices.value = next
    }
}

/** Bounded clipboard history, newest first; persisted only when enabled. */
class HistoryStore(dir: File, private val settings: () -> AppSettings) : HistoryRecorder {
    private val file = JsonFile(File(dir, "history.json"), ListSerializer(HistoryEntry.serializer()))
    private val _entries = MutableStateFlow(initial())
    val entries: StateFlow<List<HistoryEntry>> = _entries.asStateFlow()

    private fun initial(): List<HistoryEntry> {
        val s = settings()
        if (!s.historyEnabled || !s.historyPersist) {
            file.delete()
            return emptyList()
        }
        return file.read().orEmpty().take(s.historyMaxEntries)
    }

    @Synchronized
    override fun add(entry: HistoryEntry) {
        val s = settings()
        if (!s.historyEnabled) return
        set((listOf(entry) + _entries.value).take(s.historyMaxEntries))
    }

    @Synchronized
    override fun updateDelivery(eventId: String, deviceId: String, status: String) {
        val list = _entries.value
        if (list.none { it.eventId == eventId }) return
        set(list.map { if (it.eventId == eventId) it.copy(deliveries = it.deliveries + (deviceId to status)) else it })
    }

    @Synchronized
    fun delete(eventId: String) = set(_entries.value.filterNot { it.eventId == eventId })

    @Synchronized
    fun clear() {
        _entries.value = emptyList()
        file.delete()
    }

    /** Applies a settings change: disabling history or persistence erases stored data. */
    @Synchronized
    fun onSettingsChanged(s: AppSettings) {
        if (!s.historyEnabled) {
            clear()
            return
        }
        if (!s.historyPersist) file.delete() else set(_entries.value)
    }

    private fun set(list: List<HistoryEntry>) {
        _entries.value = list
        if (settings().historyPersist) runCatching { file.write(list) }
    }
}

class SettingsStore(dir: File, defaults: AppSettings) {
    private val file = JsonFile(File(dir, "settings.json"), AppSettings.serializer())
    private val _settings = MutableStateFlow(file.read() ?: defaults)
    val settings: StateFlow<AppSettings> = _settings.asStateFlow()

    val current: AppSettings get() = _settings.value

    @Synchronized
    fun update(fn: (AppSettings) -> AppSettings) {
        val next = fn(_settings.value)
        file.write(next)
        _settings.value = next
    }
}
