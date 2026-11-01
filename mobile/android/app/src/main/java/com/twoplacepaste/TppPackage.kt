package com.twoplacepaste

import com.facebook.react.ReactPackage
import com.facebook.react.bridge.NativeModule
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.uimanager.ViewManager

/** The app's own native modules: the tile bridge, the clipboard gap-fillers, saving or opening received files, the downloaded-entry cache, the third-party notices and the app's version. */
class TppPackage : ReactPackage {
    override fun createNativeModules(reactContext: ReactApplicationContext): List<NativeModule> =
        listOf(
            TileModule(reactContext),
            ClipboardModule(reactContext),
            FileModule(reactContext),
            CacheModule(reactContext),
            NoticesModule(reactContext),
            AppInfoModule(reactContext),
        )

    override fun createViewManagers(
        reactContext: ReactApplicationContext,
    ): List<ViewManager<*, *>> = emptyList()
}
