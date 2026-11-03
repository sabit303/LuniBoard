package com.universalclipboard

import android.app.Application
import com.universalclipboard.di.AppContainer
import com.universalclipboard.service.Notifications

class UniversalClipboardApp : Application() {
    lateinit var container: AppContainer
        private set

    override fun onCreate() {
        super.onCreate()
        container = AppContainer(this)
        Notifications.ensureChannels(this)
        container.start()
    }
}
