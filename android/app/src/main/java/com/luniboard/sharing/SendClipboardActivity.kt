package com.luniboard.sharing

import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.lifecycle.lifecycleScope
import com.luniboard.R
import com.luniboard.LuniBoardApp
import com.luniboard.domain.Source
import kotlinx.coroutines.launch

/**
 * Invisible activity that sends the current clipboard to the paired computers.
 *
 * Android only lets the focused app read the clipboard, so this activity is
 * briefly brought to the foreground (from the notification action or the app
 * shortcut) and reads the clipboard once it has window focus. This is an
 * explicit user action, not background monitoring.
 */
class SendClipboardActivity : ComponentActivity() {
    private var handled = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        handled = savedInstanceState?.getBoolean(KEY_HANDLED) ?: false
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putBoolean(KEY_HANDLED, handled)
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (!hasFocus || handled) return
        handled = true
        val container = (application as LuniBoardApp).container
        val clip = container.clipboard.readText()
        when {
            clip == null -> finishWith(getString(R.string.toast_clipboard_empty))
            clip.sensitive -> finishWith(getString(R.string.toast_clipboard_sensitive))
            else -> lifecycleScope.launch {
                val repo = container.repository
                val message = try {
                    if (!repo.awaitConnected(5_000)) {
                        getString(R.string.toast_no_connected)
                    } else {
                        val r = repo.send(clip.text, null, Source.CLIPBOARD)
                        val ok = r.results.values.count { it.delivered }
                        if (r.results.isEmpty()) getString(R.string.toast_no_connected)
                        else resources.getQuantityString(R.plurals.toast_sent, ok, ok, r.results.size)
                    }
                } catch (e: Exception) {
                    e.message ?: getString(R.string.toast_send_failed)
                }
                finishWith(message)
            }
        }
    }

    private fun finishWith(message: String) {
        Toast.makeText(applicationContext, message, Toast.LENGTH_SHORT).show()
        finish()
    }

    private companion object {
        const val KEY_HANDLED = "handled"
    }
}
