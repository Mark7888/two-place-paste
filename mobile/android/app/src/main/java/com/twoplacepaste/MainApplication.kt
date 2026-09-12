package com.twoplacepaste

import android.app.Application
import com.facebook.react.PackageList
import com.facebook.react.ReactApplication
import com.facebook.react.ReactHost
import com.facebook.react.ReactNativeApplicationEntryPoint.loadReactNative
import com.facebook.react.defaults.DefaultReactHost.getDefaultReactHost

/**
 * The React Native host: the autolinked packages plus this app's own.
 *
 * `by lazy` is not decoration. [TileModule] reaches the running JavaScript
 * through this host to deliver a tile tap, and a host built fresh on every
 * access would hand it a context that is always null — the tile would work
 * only on the cold-start path and silently do nothing while the app was
 * already running.
 */
class MainApplication : Application(), ReactApplication {

    override val reactHost: ReactHost by lazy {
        getDefaultReactHost(
            context = applicationContext,
            packageList = PackageList(this).packages.apply { add(TppPackage()) },
        )
    }

    override fun onCreate() {
        super.onCreate()
        loadReactNative(this)
    }
}
