package com.twoplacepaste

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import android.os.Build
import android.util.Base64
import androidx.core.content.FileProvider
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.InputStream

/**
 * The clipboard operations the JavaScript clipboard library does not cover on
 * Android.
 *
 * [readImagePNG] and [setImagePNG], because that library's image methods are
 * iOS-only in both directions: `hasImage`, `getImagePNG` and `setImage` all
 * reject on Android with "not supported on Android", which is what every sync
 * of this app's ran into. [primaryClipTimestamp], because it is what makes
 * SPEC §6's direction rule decidable on this platform: Android records when the
 * clipboard was last set, which neither macOS nor Windows does.
 *
 * All three are called only while the app is in the foreground. Android 10+
 * refuses a clipboard read to anything else, and this module does not try to be
 * an exception to that (SPEC §7.1).
 */
class ClipboardModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /**
     * readImagePNG returns what the clipboard holds as base64 PNG, or "" when
     * it holds no image at all — which is not a failure, it is the ordinary
     * case of text on the clipboard.
     *
     * Android carries an image as a `content://` URI with a read grant attached
     * to the clip, never as bytes, so this reads through the resolver rather
     * than from a path — and only for a clip that describes itself as an image,
     * because a URI to anything else is a file reference this client does not
     * carry (SPEC §7.2).
     *
     * Whatever encoding the copying app chose is re-encoded to PNG, because PNG
     * is the one image type every client in this system round-trips without a
     * colour-space argument. Bytes that are already PNG are passed through
     * untouched: a re-encode that changes nothing can still change the file.
     */
    @ReactMethod
    fun readImagePNG(promise: Promise) {
        try {
            val context = reactApplicationContext
            val clip = manager(context).primaryClip
            val uri = imageURI(clip)
            if (uri == null) {
                promise.resolve("")
                return
            }
            val png = readAsPNG(context, uri)
            if (png == null) {
                // The clip said it was an image and it could not be made into
                // one. Reporting that beats falling back to the text side,
                // which for an image clip is the URI — meaningless on the
                // device that would receive it.
                promise.reject(ERROR_CODE, "the clipboard's image could not be read")
                return
            }
            promise.resolve(Base64.encodeToString(png, Base64.NO_WRAP))
        } catch (e: TooLargeException) {
            promise.reject(
                ERROR_CODE,
                "the clipboard's image is over ${MAX_IMAGE_BYTES / (1024 * 1024)} MB, " +
                    "which is more than an entry can carry",
                e,
            )
        } catch (e: Exception) {
            // The message names the operation, never the bytes.
            promise.reject(ERROR_CODE, "the clipboard's image could not be read", e)
        }
    }

    /** imageURI is the clip's image, or null when the clipboard holds no image. */
    private fun imageURI(clip: ClipData?): Uri? {
        if (clip == null || clip.itemCount == 0) {
            return null
        }
        if (!clip.description.hasMimeType("image/*")) {
            return null
        }
        return clip.getItemAt(0).uri
    }

    /**
     * readAsPNG reads a clipboard URI and returns PNG bytes, or null when it is
     * not an image this can decode. It throws [TooLargeException] for an image
     * past the cap, before and after the re-encode: a JPEG that fits can become
     * a PNG that does not.
     */
    private fun readAsPNG(context: Context, uri: Uri): ByteArray? {
        val raw = context.contentResolver.openInputStream(uri)?.use { read(it) } ?: return null
        if (isPNG(raw)) {
            return raw
        }
        val bitmap = BitmapFactory.decodeByteArray(raw, 0, raw.size) ?: return null
        val out = ByteArrayOutputStream()
        // PNG is lossless, so the quality argument is ignored; 100 is the
        // conventional value to pass.
        if (!bitmap.compress(Bitmap.CompressFormat.PNG, 100, out)) {
            return null
        }
        if (out.size() > MAX_IMAGE_BYTES) {
            throw TooLargeException()
        }
        return out.toByteArray()
    }

    /**
     * read drains a stream, giving up past [MAX_IMAGE_BYTES] rather than
     * meeting an image the size of the heap. Bounding it here is what keeps a
     * clipboard this app did not fill from deciding how much memory it takes.
     */
    private fun read(stream: InputStream): ByteArray {
        val out = ByteArrayOutputStream()
        val buffer = ByteArray(64 * 1024)
        while (true) {
            val n = stream.read(buffer)
            if (n < 0) {
                return out.toByteArray()
            }
            if (out.size() + n > MAX_IMAGE_BYTES) {
                throw TooLargeException()
            }
            out.write(buffer, 0, n)
        }
    }

    /** TooLargeException is an image past [MAX_IMAGE_BYTES], told apart from an unreadable one. */
    private class TooLargeException : Exception()

    /** isPNG reports whether the bytes open with the PNG signature (RFC 2083 §3.1). */
    private fun isPNG(b: ByteArray): Boolean =
        b.size >= 8 &&
            b[0] == 0x89.toByte() &&
            b[1] == 0x50.toByte() &&
            b[2] == 0x4E.toByte() &&
            b[3] == 0x47.toByte() &&
            b[4] == 0x0D.toByte() &&
            b[5] == 0x0A.toByte() &&
            b[6] == 0x1A.toByte() &&
            b[7] == 0x0A.toByte()

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

        /**
         * MAX_IMAGE_BYTES bounds what is read from the clipboard. It is the
         * relay's per-entry cap (SPEC §4.3), measured on the ciphertext there
         * and on the plaintext here: past it the entry could not be sent
         * anyway, and the client says so precisely once the frame is sealed.
         */
        private const val MAX_IMAGE_BYTES = 10 * 1024 * 1024
    }
}
