package com.twoplacepaste

import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.pm.PackageInfoCompat
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * Which build of the app this is, for Settings.
 *
 * Both values are what app/build.gradle stamped into the APK: the version name
 * scripts/version.sh computed, and the version code, which is the build's stamp
 * (seconds since 2025-01-01T00:00:00Z) — the same stamp the desktop turns into
 * its "Built" date. They are read from the installed package rather than from a
 * generated BuildConfig, so the build needs no extra configuration for them.
 */
class AppInfoModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /** read resolves with `{ version, stamp }`. */
    @ReactMethod
    fun read(promise: Promise) {
        try {
            val info = packageInfo()
            val result = Arguments.createMap()
            result.putString("version", info.versionName ?: "")
            // The bridge has no long; a stamp is far below 2^53, so a double
            // carries it exactly.
            result.putDouble("stamp", PackageInfoCompat.getLongVersionCode(info).toDouble())
            promise.resolve(result)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "the app's version could not be read", e)
        }
    }

    private fun packageInfo(): PackageInfo {
        val pm = reactApplicationContext.packageManager
        val name = reactApplicationContext.packageName
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getPackageInfo(name, PackageManager.PackageInfoFlags.of(0))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(name, 0)
        }
    }

    companion object {
        const val NAME = "TppAppInfo"
        private const val ERROR_CODE = "tpp_app_info"
    }
}
