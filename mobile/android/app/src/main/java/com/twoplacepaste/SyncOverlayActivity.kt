package com.twoplacepaste

import android.os.Bundle
import com.facebook.react.ReactActivity
import com.facebook.react.ReactActivityDelegate
import com.facebook.react.defaults.DefaultNewArchitectureEntryPoint.fabricEnabled
import com.facebook.react.defaults.DefaultReactActivityDelegate

/**
 * The window a quick-settings tap opens (SPEC §7.1).
 *
 * Android has refused clipboard reads to unfocused apps since Android 10, so a
 * tile tap has to bring a window of this app forward. It does not have to be
 * the whole app, and until now it was: tapping the tile launched
 * [MainActivity], so a one-second sync cost the user their place in whatever
 * they were doing.
 *
 * This is that window instead — transparent, dimmed behind, and holding
 * nothing but a panel in the middle of the screen. It is a second React
 * surface ("TwoPlacePasteSync") on the same JavaScript context as the app, so
 * the sync it runs is the app's own session rather than a second copy of it;
 * the session is opened by the bundle itself, which is why this activity can
 * cold-start the process and still find the tap waiting to be claimed.
 *
 * What makes it a panel rather than a screen is the theme and three window
 * flags, all of them in `AppTheme.Overlay`:
 *
 *  - `windowIsTranslucent` and a transparent background, so the screen behind
 *    stays on show rather than being replaced by this app's own background;
 *  - `windowBackgroundDimEnabled`, which is the dimming — the system draws it
 *    behind this window, over whatever is underneath, which no view of ours
 *    could reach;
 *  - `windowIsFloating` is *not* set, because a floating window is measured to
 *    its content and this one has to fill the screen to catch a tap on the
 *    dimmed area.
 *
 * It is `excludeFromRecents` and its own task: a one-shot panel has no place
 * in the recents list, and it must never become the entry the launcher returns
 * to instead of the app.
 */
class SyncOverlayActivity : ReactActivity() {

    override fun getMainComponentName(): String = COMPONENT_NAME

    override fun createReactActivityDelegate(): ReactActivityDelegate =
        DefaultReactActivityDelegate(this, mainComponentName, fabricEnabled)

    override fun onCreate(savedInstanceState: Bundle?) {
        // Registered before the React surface mounts, so a JavaScript side that
        // finishes its sync faster than this window can draw still closes it.
        current = this
        super.onCreate(savedInstanceState)
    }

    override fun onDestroy() {
        if (current === this) {
            current = null
        }
        super.onDestroy()
    }

    /**
     * finishQuietly closes the panel with no transition.
     *
     * `overridePendingTransition` is deliberate: an animation would draw this
     * app sliding away over a screen it never belonged to, and the panel's job
     * is to be gone the moment it has nothing left to say.
     */
    @Suppress("DEPRECATION")
    private fun finishQuietly() {
        if (!isFinishing) {
            finish()
            overridePendingTransition(0, 0)
        }
    }

    companion object {
        /** COMPONENT_NAME is registered in `index.js`; the two must agree. */
        const val COMPONENT_NAME = "TwoPlacePasteSync"

        /**
         * current is the panel on screen, if there is one. It is set from the
         * main thread in onCreate and cleared in onDestroy, and read from the
         * React Native thread by [TileModule.closeOverlay], which is why it is
         * volatile.
         */
        @Volatile
        private var current: SyncOverlayActivity? = null

        /** isShowing reports whether a panel is on screen right now. */
        fun isShowing(): Boolean = current != null

        /** close dismisses the panel, if one is showing. It is safe to call when none is. */
        fun close() {
            val activity = current ?: return
            activity.runOnUiThread { activity.finishQuietly() }
        }
    }
}
