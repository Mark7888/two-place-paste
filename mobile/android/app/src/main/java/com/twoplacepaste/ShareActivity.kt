package com.twoplacepaste

import android.content.Intent
import android.os.Bundle
import com.facebook.react.ReactActivity
import com.facebook.react.ReactActivityDelegate
import com.facebook.react.defaults.DefaultNewArchitectureEntryPoint.fabricEnabled
import com.facebook.react.defaults.DefaultReactActivityDelegate

/**
 * The window "Share → TwoPlacePaste" opens: one file, one image or a piece of
 * text another app handed this one with `ACTION_SEND`.
 *
 * It is a panel over the sharing app rather than the whole app, built the same
 * way as [SyncOverlayActivity] — the same transparent theme, and a second React
 * surface ("TwoPlacePasteShare") on the app's one JavaScript context, so what
 * runs is the app's own session. Nothing is uploaded on arrival: the panel asks
 * whether to send it to the group or save it to the store, and the user can
 * just as well dismiss it.
 *
 * What was shared is recorded with [ShareModule] *before* the React surface
 * mounts, so a cold start finds it waiting to be claimed, exactly as a tile
 * tap is. The read grant that came with a shared file lasts as long as this
 * activity, which is why the file is read only while the panel is up.
 *
 * Its own task and `excludeFromRecents`, like the tile's panel: a one-shot
 * panel has no place in the recents list and must never become what the
 * launcher returns to instead of the app.
 */
class ShareActivity : ReactActivity() {

    override fun getMainComponentName(): String = COMPONENT_NAME

    override fun createReactActivityDelegate(): ReactActivityDelegate =
        DefaultReactActivityDelegate(this, mainComponentName, fabricEnabled)

    override fun onCreate(savedInstanceState: Bundle?) {
        current = this
        // A restored activity (the process was killed while it was in the
        // background) has no grant left to read with, so only a fresh launch
        // is taken in.
        if (savedInstanceState == null) {
            ShareModule.receive(applicationContext, intent)
        }
        super.onCreate(savedInstanceState)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        // A second share while the panel is up replaces the first.
        ShareModule.receive(applicationContext, intent)
    }

    override fun onDestroy() {
        if (current === this) {
            current = null
        }
        if (isFinishing) {
            // Backed out of, or closed: the grant goes with this activity, so
            // the share goes too.
            ShareModule.drop()
        }
        super.onDestroy()
    }

    /** finishQuietly closes the panel with no transition, as the tile's panel does. */
    @Suppress("DEPRECATION")
    private fun finishQuietly() {
        if (!isFinishing) {
            finish()
            overridePendingTransition(0, 0)
        }
    }

    companion object {
        /** COMPONENT_NAME is registered in `index.js`; the two must agree. */
        const val COMPONENT_NAME = "TwoPlacePasteShare"

        @Volatile
        private var current: ShareActivity? = null

        /** close dismisses the panel, if one is showing. It is safe to call when none is. */
        fun close() {
            val activity = current ?: return
            activity.runOnUiThread { activity.finishQuietly() }
        }
    }
}
