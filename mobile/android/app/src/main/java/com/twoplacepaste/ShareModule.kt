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
import java.io.File

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
     * stage copies the shared file into this app's cache, within [maxBytes],
     * and resolves with `{ name, contentType, size }`, or with `{ name, size,
     * tooLarge: true }` when it is over the limit. The bytes are then read
     * with [readChunk], a few megabytes at a time, and [discard]ed.
     *
     * It rejects when the share [id] names is no longer the one waiting — the
     * panel was closed, or another share replaced it — and when the file
     * cannot be read, with a message that names neither the file nor the URI.
     */
    @ReactMethod
    fun stage(id: Double, maxBytes: Double, promise: Promise) {
        val wanted = id.toInt()
        val shared = synchronized(Companion) { current?.takeIf { it.id == wanted } }
        val uri = shared?.uri
        if (shared == null || uri == null) {
            promise.reject(ERROR_CODE, "the shared file is no longer available")
            return
        }
        val context = reactApplicationContext
        val name = shared.name ?: ""
        // Off the main thread, and off the module thread: copying a large file
        // takes a while, and nothing else should wait on it.
        Thread {
            val dest = stagedFile(context)
            try {
                synchronized(Companion) { stagedId = 0 }
                dest.delete()
                val meta = ContentReader.meta(context, uri)
                val limit = maxBytes.toLong()
                // Refused before the copy when the provider says it will not fit.
                val size = if (meta.size > limit) null else ContentReader.stage(context, uri, limit, dest)
                if (size == null) {
                    promise.resolve(
                        Arguments.createMap().apply {
                            putString("name", name)
                            putDouble("size", maxOf(meta.size, limit + 1).toDouble())
                            putBoolean("tooLarge", true)
                        },
                    )
                    return@Thread
                }
                val stillCurrent = synchronized(Companion) {
                    if (current?.id == wanted) {
                        stagedId = wanted
                        true
                    } else {
                        false
                    }
                }
                if (!stillCurrent) {
                    dest.delete()
                    promise.reject(ERROR_CODE, "the shared file is no longer available")
                    return@Thread
                }
                promise.resolve(
                    Arguments.createMap().apply {
                        putString("name", name)
                        putString("contentType", shared.type.ifEmpty { FileModule.mimeOf("", name) })
                        putDouble("size", size.toDouble())
                    },
                )
            } catch (e: Throwable) {
                // Throwable, not Exception: running out of memory or disk must
                // be a refusal the panel shows, never a crash.
                dest.delete()
                promise.reject(ERROR_CODE, "the shared file could not be read", e)
            }
        }.start()
    }

    /**
     * readChunk resolves with [length] bytes of the staged file from
     * [offset], as base64. It rejects when share [id] is not the one staged.
     */
    @ReactMethod
    fun readChunk(id: Double, offset: Double, length: Double, promise: Promise) {
        val staged = synchronized(Companion) { stagedId == id.toInt() && stagedId != 0 }
        if (!staged) {
            promise.reject(ERROR_CODE, "the shared file is no longer available")
            return
        }
        try {
            promise.resolve(ContentReader.chunk(stagedFile(reactApplicationContext), offset.toLong(), length.toInt()))
        } catch (e: Throwable) {
            promise.reject(ERROR_CODE, "the shared file could not be read", e)
        }
    }

    /** discard deletes the staged copy of share [id], once it has been read. */
    @ReactMethod
    fun discard(id: Double) {
        val wanted = id.toInt()
        val was = synchronized(Companion) {
            if (stagedId == wanted) {
                stagedId = 0
                true
            } else {
                false
            }
        }
        if (was) {
            stagedFile(reactApplicationContext).delete()
        }
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
                stagedId = 0
                true
            } else {
                false
            }
        }
        if (!matched) {
            return
        }
        stagedFile(reactApplicationContext).delete()
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
        drop(context)
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

        /** stagedId is the share whose copy is in [stagedFile], or 0 for none. */
        private var stagedId = 0

        /**
         * stagedFile is where a shared file is copied while it is read: one
         * file, in the cache, which no provider of this app serves. Each new
         * share, finish and drop deletes it.
         */
        private fun stagedFile(context: Context) = File(File(context.cacheDir, STAGE_DIRECTORY), STAGE_NAME)

        private const val STAGE_DIRECTORY = "shared"
        private const val STAGE_NAME = "current"

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
            stagedFile(context).delete()
            synchronized(this) {
                stagedId = 0
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
        fun drop(context: Context) {
            synchronized(this) {
                current = null
                delivered = true
                stagedId = 0
            }
            stagedFile(context).delete()
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
