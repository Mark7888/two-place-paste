package com.twoplacepaste

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.widget.Toast
import androidx.core.content.IntentCompat
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import com.facebook.react.modules.core.DeviceEventManagerModule

/**
 * The bridge between [ShareActivity] and the JavaScript that asks the user
 * what to do with what was shared.
 *
 * It works like [TileModule]: a share can find the app running (an event is
 * emitted and JavaScript claims it) or starting (JavaScript claims it once,
 * during startup). Each share is claimed exactly once.
 *
 * Unlike a tile tap, a share carries something — a `content://` URI or text —
 * and the URI stays here. JavaScript is told its name, type and size, and asks
 * for the bytes only once the user has chosen to send or store it, with the
 * limit that destination allows. The shared URI is never handed to JavaScript
 * as a way to read it, except as an image preview.
 *
 * Only `content://` URIs from another app are read. A `file://` URI, or a
 * `content://` URI served by this app's own providers, is refused: either
 * would let the sending app make this one read — and upload — its own private
 * files.
 */
class ShareModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /**
     * consumePending resolves with the share waiting to be claimed, or null.
     * It runs on the native modules thread, so the provider is asked for the
     * file's name and size here rather than on the main thread when it arrived.
     *
     * The shape is one of
     *  - `{ id, kind: "file", name, contentType, size, previewUri }` (size is -1 when unknown);
     *  - `{ id, kind: "text", text, subject }`;
     *  - `{ id, kind: "unsupported" }` — something this app will not read.
     */
    @ReactMethod
    fun consumePending(promise: Promise) {
        val shared = claim()
        if (shared == null) {
            promise.resolve(null)
            return
        }
        try {
            promise.resolve(describe(shared))
        } catch (e: Exception) {
            // A provider that cannot even say what the file is. Reported as
            // something this app cannot take, never with the URI or the name.
            promise.resolve(
                Arguments.createMap().apply {
                    putInt("id", shared.id)
                    putString("kind", KIND_UNSUPPORTED)
                },
            )
        }
    }

    private fun describe(shared: Shared) = Arguments.createMap().apply {
        putInt("id", shared.id)
        val uri = shared.uri
        when {
            uri != null -> {
                val context = reactApplicationContext
                val meta = ContentReader.meta(context, uri)
                val mime = FileModule.mimeOf(
                    context.contentResolver.getType(uri) ?: shared.type,
                    meta.name,
                )
                val name = FileModule.safeName(meta.name, mime)
                synchronized(Companion) {
                    if (current?.id == shared.id) {
                        current = shared.copy(name = name, type = mime)
                    }
                }
                putString("kind", KIND_FILE)
                putString("name", name)
                putString("contentType", mime)
                putDouble("size", meta.size.toDouble())
                putString("previewUri", if (mime.startsWith("image/")) uri.toString() else "")
            }
            shared.text != null -> {
                putString("kind", KIND_TEXT)
                putString("text", shared.text)
                putString("subject", shared.subject)
            }
            else -> putString("kind", KIND_UNSUPPORTED)
        }
    }

    /**
     * read resolves with the shared file's bytes, in the same shape as
     * [FileModule.pickFile]: `{ name, contentType, size, base64 }`, or
     * `{ name, size, tooLarge: true }` when it is over [maxBytes]. It rejects
     * when the share [id] names is no longer the one waiting — the panel was
     * closed, or another share replaced it.
     */
    @ReactMethod
    fun read(id: Double, maxBytes: Double, promise: Promise) {
        val shared = synchronized(Companion) { current?.takeIf { it.id == id.toInt() } }
        val uri = shared?.uri
        if (shared == null || uri == null) {
            promise.reject(ERROR_CODE, "the shared file is no longer available")
            return
        }
        val context = reactApplicationContext
        // Off the main thread: a file can be tens of megabytes.
        Thread {
            ContentReader.read(context, uri, maxBytes.toLong(), promise, shared.name, shared.type.ifEmpty { null })
        }.start()
    }

    /**
     * finish closes the share panel for share [id] and forgets the share. A
     * [message] that is not empty is shown as a toast over the app the user
     * returns to; it is a status — "Sent to the group." — and never the
     * shared content or its name, because a toast is visible over other apps.
     *
     * A finish for a share that has since been replaced does nothing, so an
     * upload that ends after the user shared something else does not close
     * the panel that is asking about the new one. Id 0 closes the panel
     * whatever it shows: it is how a panel with no share on it is dismissed.
     */
    @ReactMethod
    fun finish(id: Double, message: String) {
        val wanted = id.toInt()
        val matched = synchronized(Companion) {
            if (wanted == 0 || current?.id == wanted) {
                current = null
                delivered = true
                true
            } else {
                false
            }
        }
        if (!matched) {
            return
        }
        if (message.isNotEmpty()) {
            val context = reactApplicationContext
            Handler(Looper.getMainLooper()).post {
                Toast.makeText(context, message, Toast.LENGTH_SHORT).show()
            }
        }
        ShareActivity.close()
    }

    /** openApp brings the app itself forward — to pair, say — and closes the panel. */
    @ReactMethod
    fun openApp() {
        val context = reactApplicationContext
        val launch = context.packageManager.getLaunchIntentForPackage(context.packageName)
        if (launch != null) {
            launch.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            context.startActivity(launch)
        }
        drop()
        ShareActivity.close()
    }

    /** addListener and removeListeners exist because NativeEventEmitter requires them. */
    @ReactMethod
    fun addListener(eventName: String) = Unit

    @ReactMethod
    fun removeListeners(count: Int) = Unit

    /**
     * Shared is one share. [uri] is set for a file, [text] for text, and
     * neither for something this app refused. [name] and [type] are filled in
     * when JavaScript claims it, so that a later read uses the same ones the
     * panel showed.
     */
    data class Shared(
        val id: Int,
        val uri: Uri?,
        val type: String,
        val text: String?,
        val subject: String,
        val name: String? = null,
    )

    companion object {
        const val NAME = "TppShare"

        /** SHARE_EVENT is what a running app listens for. */
        const val SHARE_EVENT = "TppShareReceived"

        private const val ERROR_CODE = "tpp_share"
        private const val KIND_FILE = "file"
        private const val KIND_TEXT = "text"
        private const val KIND_UNSUPPORTED = "unsupported"

        private var nextId = 1

        /** current is the share the panel is about, from arrival until it is finished or dropped. */
        private var current: Shared? = null

        /** delivered is whether JavaScript has claimed [current] yet. */
        private var delivered = true

        /**
         * receive records what an `ACTION_SEND` intent carries and tells a
         * running app about it. A file wins over text when an app sends both
         * (a photo with a caption, say), because the file is what was shared.
         */
        fun receive(context: Context, intent: Intent?) {
            if (intent?.action != Intent.ACTION_SEND) {
                return
            }
            val stream = IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java)
            val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
            val subject = intent.getCharSequenceExtra(Intent.EXTRA_SUBJECT)?.toString() ?: ""
            synchronized(this) {
                val id = nextId++
                current = when {
                    stream != null && readable(context, stream) ->
                        Shared(id, stream, intent.type ?: "", null, subject)
                    stream == null && !text.isNullOrEmpty() ->
                        Shared(id, null, "", text, subject)
                    else -> Shared(id, null, "", null, "")
                }
                delivered = false
            }
            val host = (context.applicationContext as? MainApplication)?.reactHost
            val reactContext = host?.currentReactContext ?: return
            reactContext
                .getJSModule(DeviceEventManagerModule.RCTDeviceEventEmitter::class.java)
                .emit(SHARE_EVENT, null)
        }

        /** drop forgets the share, if there is one: the panel is gone, and its grant with it. */
        @Synchronized
        fun drop() {
            current = null
            delivered = true
        }

        /** claim hands a waiting share to exactly one caller. */
        @Synchronized
        private fun claim(): Shared? {
            if (delivered) {
                return null
            }
            delivered = true
            return current
        }

        /**
         * readable reports whether [uri] is one this app may read on the
         * sender's behalf: `content://`, and not served by this app itself.
         */
        @Suppress("DEPRECATION") // The flags-object overload is API 33+; this app runs on 24.
        private fun readable(context: Context, uri: Uri): Boolean {
            if (uri.scheme != ContentResolver.SCHEME_CONTENT) {
                return false
            }
            // An authority can carry a user ("10@com.example.provider") for a
            // share across profiles; the provider is the part after it, and
            // that is what is checked, or "0@" would get this app's own
            // provider past the check.
            val authority = uri.authority?.substringAfterLast('@') ?: return false
            if (authority.isEmpty()) {
                return false
            }
            val provider = context.packageManager.resolveContentProvider(authority, 0)
            return provider?.packageName != context.packageName
        }
    }
}
