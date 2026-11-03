package com.universalclipboard.clipboard

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import com.universalclipboard.domain.ClipboardWriter
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** Clipboard content read while the app is in the foreground. */
data class LocalClip(val text: String, val sensitive: Boolean)

/**
 * Android clipboard access.
 *
 * Writing is allowed from the background (e.g. the foreground service).
 * Reading is restricted since Android 10 to the app that currently has input
 * focus, so [readText] must only be called from a focused activity; it
 * returns null otherwise. The app never tries to work around this.
 */
class AndroidClipboard(private val context: Context) : ClipboardWriter {
    private val manager = context.getSystemService(ClipboardManager::class.java)

    override suspend fun writeText(text: String, fromDeviceName: String) = withContext(Dispatchers.Main) {
        manager.setPrimaryClip(ClipData.newPlainText("From $fromDeviceName", text))
    }

    fun readText(): LocalClip? {
        val clip = runCatching { manager.primaryClip }.getOrNull() ?: return null
        if (clip.itemCount == 0) return null
        val text = clip.getItemAt(0).coerceToText(context)?.toString().orEmpty()
        if (text.isEmpty()) return null
        val sensitive = Build.VERSION.SDK_INT >= 33 &&
            clip.description.extras?.getBoolean(ClipDescription.EXTRA_IS_SENSITIVE) == true
        return LocalClip(text, sensitive)
    }

    /** Copies text from the history screen; marks nothing as sensitive. */
    fun copy(text: String) {
        manager.setPrimaryClip(ClipData.newPlainText("Universal Clipboard", text))
    }
}
