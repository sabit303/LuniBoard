package com.luniboard

import android.app.Application
import com.luniboard.di.AppContainer
import com.luniboard.service.Notifications

class LuniBoardApp : Application() {
    lateinit var container: AppContainer
        private set

    override fun onCreate() {
        super.onCreate()
        container = AppContainer(this)
        Notifications.ensureChannels(this)
        container.start()
    }
}
