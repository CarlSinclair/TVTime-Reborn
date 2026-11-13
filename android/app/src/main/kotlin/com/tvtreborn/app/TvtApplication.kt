package com.tvtreborn.app

import android.app.Application
import com.csuit.shell.Shell
import com.csuit.shell.ShellConfig

class TvtApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        Shell.install(
            this,
            ShellConfig(
                appName = getString(R.string.app_name),
                hostPrefix = "https://",
                hostSuffix = ".carl-sinclair.com",
                pushChannelName = getString(R.string.push_channel),
                youTubeSync = true,
                mediaScrobble = true,
            ),
        )
    }
}
