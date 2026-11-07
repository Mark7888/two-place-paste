package com.twoplacepaste

import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * JavaScript's handle on background uploads: start [TransferService], move its
 * notification along, and say how each upload ended.
 *
 * Every title and message here is a status — "Encrypting…", "Sent to the
 * group." — and never what is being sent or its name: notifications show on
 * the lock screen.
 */
class TransferModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /**
     * start starts the background service. It must be called while a window
     * of the app is in the foreground — the share panel — because Android
     * refuses to start a foreground service from the background.
     */
    @ReactMethod
    fun start(promise: Promise) {
        try {
            TransferService.start(reactApplicationContext)
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "the background upload could not be started", e)
        }
    }

    /**
     * progress updates the ongoing notification: [title], a bar at
     * [done]/[total] (0 is a moving bar), and [detail] — "2 of 3" — when the
     * run holds more than one upload.
     */
    @ReactMethod
    fun progress(title: String, detail: String, done: Double, total: Double) {
        TransferNotifications.update(reactApplicationContext, title, detail, done.toLong(), total.toLong())
    }

    /** result posts how upload [id] ended, in a notification of its own. */
    @ReactMethod
    fun result(id: Double, ok: Boolean, message: String) {
        TransferNotifications.result(reactApplicationContext, id.toInt(), ok, message)
    }

    companion object {
        const val NAME = "TppTransfer"
        private const val ERROR_CODE = "tpp_transfer"
    }
}
