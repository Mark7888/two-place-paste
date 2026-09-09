package com.twoplacepaste

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.util.Base64
import androidx.core.content.FileProvider
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import java.io.File

/**
 * The two clipboard operations the JavaScript clipboard library does not cover
 * on Android.
 *
 * [setImagePNG], because that library writes images on iOS only, and an image
 * that can be received but not pasted is half a feature. [primaryClipTimestamp],
 * because it is what makes SPEC §6's direction rule decidable on this platform:
 * Android records when the clipboard was last set, which neither macOS nor
 * Windows does.
 *
 * Both are called only while the app is in the foreground. Android 10+ refuses
 * a clipboard read to anything else, and this module does not try to be an
 * exception to that (SPEC §7.1).
 */
class ClipboardModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /**
     * setImagePNG stages the PNG in this app's cache and puts a content URI on
     * the clipboard, with the read grant attached to the clip itself. The
     * provider is not exported: without the grant nothing can read the file.
     */
    @ReactMethod
    fun setImagePNG(base64: String, promise: Promise) {
        try {
            val context = reactApplicationContext
            val directory = File(context.cacheDir, CLIP_DIRECTORY)
            if (!directory.exists() && !directory.mkdirs()) {
                promise.reject(ERROR_CODE, "the clipboard staging directory could not be created")
                return
            }
            // One file, replaced every time: a received image is transient, and
            // keeping a history of them in the cache would be a second copy of
            // the group's data nobody asked for.
            val file = File(directory, CLIP_FILE)
            file.outputStream().use { it.write(Base64.decode(base64, Base64.DEFAULT)) }

            val uri = FileProvider.getUriForFile(
                context,
                "${context.packageName}.clips",
                file,
            )
            val clip = ClipData.newUri(context.contentResolver, "TwoPlacePaste", uri)
            manager(context).setPrimaryClip(clip)
            promise.resolve(null)
        } catch (e: Exception) {
            // The message names the operation, never the bytes.
            promise.reject(ERROR_CODE, "the image could not be placed on the clipboard", e)
        }
    }

    /**
     * primaryClipTimestamp is when the clipboard was last set, in UTC
     * milliseconds, or 0 when the platform will not say — which is what the app
     * treats as "ask the user for a direction" (SPEC §6).
     */
    @ReactMethod
    fun primaryClipTimestamp(promise: Promise) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            promise.resolve(0.0)
            return
        }
        val description = manager(reactApplicationContext).primaryClipDescription
        promise.resolve((description?.timestamp ?: 0L).toDouble())
    }

    private fun manager(context: Context): ClipboardManager =
        context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager

    companion object {
        const val NAME = "TppClipboard"
        private const val CLIP_DIRECTORY = "clips"
        private const val CLIP_FILE = "clip.png"
        private const val ERROR_CODE = "tpp_clipboard"
    }
}
