package com.twoplacepaste

import android.content.Intent
import com.facebook.react.ReactActivity
import com.facebook.react.ReactActivityDelegate
import com.facebook.react.defaults.DefaultNewArchitectureEntryPoint.fabricEnabled
import com.facebook.react.defaults.DefaultReactActivityDelegate

/**
 * The app itself.
 *
 * It is no longer what the quick-settings tile starts — that is
 * [SyncOverlayActivity], a panel rather than a screen — but the tile's action
 * can still reach here when the app is launched with it, so the same claim is
 * made: the sync is driven from JavaScript, which picks up the pending request
 * [TileModule] has recorded.
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
