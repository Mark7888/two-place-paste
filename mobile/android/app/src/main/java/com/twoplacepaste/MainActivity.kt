package com.twoplacepaste

import android.content.Intent
import com.facebook.react.ReactActivity
import com.facebook.react.ReactActivityDelegate
import com.facebook.react.defaults.DefaultNewArchitectureEntryPoint.fabricEnabled
import com.facebook.react.defaults.DefaultReactActivityDelegate

/**
 * The app's one activity.
 *
 * `singleTask` in the manifest means a tile tap on a running app arrives here
 * as [onNewIntent] rather than as a second instance; either way the sync is
 * driven from JavaScript, which claims the pending request [TileModule] has
 * recorded.
 */
class MainActivity : ReactActivity() {

    override fun getMainComponentName(): String = "TwoPlacePaste"

    override fun createReactActivityDelegate(): ReactActivityDelegate =
        DefaultReactActivityDelegate(this, mainComponentName, fabricEnabled)

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (intent.action == SyncTileService.ACTION_TILE_SYNC) {
            // The app is already up, so the event reaches JavaScript now. A
            // cold start finds the same request waiting instead.
            TileModule.requestSync(applicationContext)
        }
    }
}
