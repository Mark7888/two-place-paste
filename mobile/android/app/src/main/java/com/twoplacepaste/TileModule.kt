package com.twoplacepaste

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.service.quicksettings.TileService
import android.widget.Toast
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import com.facebook.react.modules.core.DeviceEventManagerModule

/**
 * The bridge between the quick-settings tile and the JavaScript that does the
 * sync (SPEC §7.1).
 *
 * A tap can find the app in either state, so there are two paths and one piece
 * of state:
 *
 *  - the app is already running: an event is emitted and JavaScript reacts;
 *  - the app is starting: JavaScript asks once, during startup, whether a tap
 *    is waiting, and claims it.
 *
 * The pending flag is claimed exactly once, so a tap can never produce two
 * syncs, and a launch that was not a tile tap never produces one.
 */
class TileModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /** consumePendingRequest reports whether a tile tap is waiting, and clears it. */
    @ReactMethod
    fun consumePendingRequest(promise: Promise) {
        promise.resolve(claimPending())
    }

    /**
     * report tells the tile how the sync went. The message is a status —
     * "Copied 12 bytes to this device" — and never clipboard content: a toast
     * is visible over other apps and on the lock screen's shade.
     *
     * The toast is skipped while the panel is on screen. The panel is already
     * showing the outcome, in a place the user can read at their own pace and
     * dismiss themselves; a toast on top of it says the same thing twice and
     * takes the failure away again after two seconds.
     */
    @ReactMethod
    fun report(ok: Boolean, message: String) {
        lastOutcome = Outcome(ok, message)
        val context = reactApplicationContext
        if (!SyncOverlayActivity.isShowing()) {
            Handler(Looper.getMainLooper()).post {
                Toast.makeText(context, message, Toast.LENGTH_SHORT).show()
            }
        }
        // Ask the system to re-bind the tile so it redraws with the outcome.
        TileService.requestListeningState(
            context,
            android.content.ComponentName(context, SyncTileService::class.java),
        )
    }

    /**
     * closeOverlay dismisses the panel a tile tap opened.
     *
     * JavaScript calls it when the sync it was opened for has finished or the
     * user has declined it. It is a no-op when no panel is showing — a sync
     * started from inside the app reaches the same code and must not close
     * anything.
     */
    @ReactMethod
    fun closeOverlay() {
        SyncOverlayActivity.close()
    }

    /** addListener and removeListeners exist because NativeEventEmitter requires them. */
    @ReactMethod
    fun addListener(eventName: String) = Unit

    @ReactMethod
    fun removeListeners(count: Int) = Unit

    /** Outcome is the last sync's result, which the tile renders as its subtitle. */
    data class Outcome(val ok: Boolean, val label: String)

    companion object {
        const val NAME = "TppTile"

        /** TILE_REQUEST_EVENT is what a running app listens for. */
        const val TILE_REQUEST_EVENT = "TppTileSyncRequested"

        @Volatile
        var lastOutcome: Outcome? = null
            internal set

        @Volatile
        private var pending: Boolean = false

        /**
         * requestSync records a tap. If the app is already running, the event
         * reaches it immediately and the flag is cleared by the app claiming
         * it; if it is not, the flag waits for startup.
         */
        fun requestSync(context: Context) {
            pending = true
            val host = (context.applicationContext as? MainApplication)?.reactHost
            val reactContext = host?.currentReactContext ?: return
            reactContext
                .getJSModule(DeviceEventManagerModule.RCTDeviceEventEmitter::class.java)
                .emit(TILE_REQUEST_EVENT, null)
        }

        /** claimPending hands a waiting tap to exactly one caller. */
        @Synchronized
        fun claimPending(): Boolean {
            val was = pending
            pending = false
            return was
        }
    }
}
