package com.twoplacepaste

import android.util.Base64
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import java.io.File

/**
 * The files under the downloaded-entry cache (`src/core/protocol/entryCache.ts`).
 *
 * This is storage and nothing else: what is kept, for how long and how much
 * of it is decided on the JavaScript side, so the rules are the same code on
 * every platform and are tested there. What lands here is each entry's sealed
 * container — still encrypted under the group key — and a small index beside
 * it. It lives in the app's cache directory, which Android may clear whenever
 * it is short of space; the cache is built to find files gone.
 *
 * Only plain names inside the one directory are accepted, so nothing passed
 * in can reach a file outside it.
 */
class CacheModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    private fun directory(): File = File(reactApplicationContext.cacheDir, DIRECTORY)

    private fun file(name: String): File? =
        if (SAFE_NAME.matches(name)) File(directory(), name) else null

    /** read resolves with the file as base64, or null when there is no such file. */
    @ReactMethod
    fun read(name: String, promise: Promise) {
        try {
            val f = file(name)
            if (f == null || !f.isFile) {
                promise.resolve(null)
                return
            }
            promise.resolve(Base64.encodeToString(f.readBytes(), Base64.NO_WRAP))
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "a cached entry could not be read", e)
        }
    }

    /**
     * write replaces a file. It writes to a temporary name and renames it, so
     * a file is either the old bytes or the new ones, never half of each.
     */
    @ReactMethod
    fun write(name: String, base64: String, promise: Promise) {
        try {
            val f = file(name)
            if (f == null) {
                promise.reject(ERROR_CODE, "not a cache file name")
                return
            }
            val dir = directory()
            if (!dir.exists() && !dir.mkdirs()) {
                promise.reject(ERROR_CODE, "the cache directory could not be created")
                return
            }
            val tmp = File(dir, "$name.tmp")
            tmp.writeBytes(Base64.decode(base64, Base64.DEFAULT))
            if (!tmp.renameTo(f)) {
                tmp.delete()
                promise.reject(ERROR_CODE, "a cached entry could not be written")
                return
            }
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "a cached entry could not be written", e)
        }
    }

    @ReactMethod
    fun remove(name: String, promise: Promise) {
        file(name)?.delete()
        promise.resolve(null)
    }

    /** list resolves with every file in the cache directory and its size. */
    @ReactMethod
    fun list(promise: Promise) {
        val out = Arguments.createArray()
        directory().listFiles()?.forEach { f ->
            if (f.isFile) {
                out.pushMap(
                    Arguments.createMap().apply {
                        putString("name", f.name)
                        putDouble("size", f.length().toDouble())
                    },
                )
            }
        }
        promise.resolve(out)
    }

    companion object {
        const val NAME = "TppCache"
        private const val DIRECTORY = "entries"
        private const val ERROR_CODE = "tpp_cache"
        private val SAFE_NAME = Regex("^[A-Za-z0-9_-]{1,128}\\.(bin|json|bin\\.tmp|json\\.tmp)$")
    }
}
