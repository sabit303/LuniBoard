package com.luniboard.ui.theme

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

private val Light = lightColorScheme(primary = Color(0xFF2F5DA8), secondary = Color(0xFF545F71), tertiary = Color(0xFF2E7D5B))
private val Dark = darkColorScheme(primary = Color(0xFFAAC7FF), secondary = Color(0xFFBCC7DB), tertiary = Color(0xFF8FD5B0))

@Composable
fun LuniBoardTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    val context = LocalContext.current
    val scheme = when {
        Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        dark -> Dark
        else -> Light
    }
    MaterialTheme(colorScheme = scheme, content = content)
}

/** Colour used for the "connected" indicator. */
val ConnectedGreen = Color(0xFF2E9D5B)
