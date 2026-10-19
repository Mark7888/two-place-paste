package com.twoplacepaste

import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import java.io.FileNotFoundException

/**
 * The third-party notices the app ships with (LICENSING.md).
 *
 * The build writes them into the APK's assets: app/build.gradle resolves the
 * Android libraries the variant is built from and scripts/notices adds the npm
 * packages. Settings reads them from here to show them.
 */
class NoticesModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /** read resolves with the notices text, or null when this build carries none. */
    @ReactMethod
    fun read(promise: Promise) {
        try {
            val text = reactApplicationContext.assets.open(ASSET).bufferedReader().use { it.readText() }
            promise.resolve(text)
        } catch (e: FileNotFoundException) {
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "the third-party notices could not be read", e)
        }
    }

    companion object {
        const val NAME = "TppNotices"
        private const val ASSET = "third_party_notices.txt"
        private const val ERROR_CODE = "tpp_notices"
    }
}
