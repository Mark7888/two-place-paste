package com.twoplacepaste

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.media.MediaScannerConnection
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.provider.OpenableColumns
import android.util.Base64
import android.webkit.MimeTypeMap
import androidx.annotation.RequiresApi
import androidx.core.content.FileProvider
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.BaseActivityEventListener
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import java.io.File

/**
 * What happens to a received entry the Android clipboard cannot hold — a file
 * from a desktop, a PDF, an archive — or an image the user chose not to
 * paste. The user picks one of three things:
 *
 *  - [saveToDownloads] keeps a copy in Downloads/TwoPlacePaste;
 *  - [open] hands it to whichever app opens that type, and keeps nothing;
 *  - [share] hands it to the share sheet, and keeps nothing.
 *
 * Opening and sharing do not go through Downloads. The bytes are staged in this app's
 * cache and handed over as a FileProvider `content://` URI with a one-off read
 * grant, the same way a received image goes onto the clipboard. Each open or
 * share replaces the last one, so the cache never holds more than one file.
 *
 * It also goes the other way: [pickFile] lets the user choose a file to save
 * to the group's store, through the system's own picker.
 */
class FileModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /** The pick in progress, and the most it may read. One at a time. */
    private var pickPromise: Promise? = null
    private var pickMaxBytes: Long = 0

    private val pickListener = object : BaseActivityEventListener() {
        override fun onActivityResult(activity: Activity, requestCode: Int, resultCode: Int, data: Intent?) {
            if (requestCode != PICK_REQUEST) {
                return
            }
            val promise = pickPromise ?: return
            pickPromise = null
            val uri = data?.data
            if (resultCode != Activity.RESULT_OK || uri == null) {
                // Backing out of the picker is an answer, not a failure.
                promise.resolve(null)
                return
            }
            val max = pickMaxBytes
            // Off the main thread: a file can be tens of megabytes.
            Thread { readPicked(uri, max, promise) }.start()
        }
    }

    init {
        reactContext.addActivityEventListener(pickListener)
    }

    /**
     * pickFile opens the system's file picker and resolves with the chosen
     * file as `{ name, contentType, size, base64 }`, with `{ name, size,
     * tooLarge: true }` and no bytes when it is over [maxBytes], or with null
     * when the user backed out. Only the file the user picked is read, and
     * only through the one-off grant the picker hands this app.
     */
    @ReactMethod
    fun pickFile(maxBytes: Double, promise: Promise) {
        val activity = reactApplicationContext.currentActivity
        if (activity == null) {
            promise.reject(ERROR_CODE, "the file picker needs the app in the foreground")
            return
        }
        if (pickPromise != null) {
            promise.reject(ERROR_CODE, "a file is already being picked")
            return
        }
        val intent = Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
            addCategory(Intent.CATEGORY_OPENABLE)
            type = "*/*"
        }
        pickPromise = promise
        pickMaxBytes = maxBytes.toLong()
        try {
            activity.startActivityForResult(intent, PICK_REQUEST)
        } catch (e: ActivityNotFoundException) {
            pickPromise = null
            promise.reject(ERROR_CODE, "this device has no file picker", e)
        }
    }

    private fun readPicked(uri: Uri, maxBytes: Long, promise: Promise) {
        try {
            val resolver = reactApplicationContext.contentResolver
            var name = ""
            var size = -1L
            resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use {
                if (it.moveToFirst()) {
                    name = it.getString(0) ?: ""
                    if (!it.isNull(1)) {
                        size = it.getLong(1)
                    }
                }
            }
            if (size > maxBytes) {
                promise.resolve(
                    Arguments.createMap().apply {
                        putString("name", name)
                        putDouble("size", size.toDouble())
                        putBoolean("tooLarge", true)
                    },
                )
                return
            }
            // The size a provider reports can be missing or wrong, so the read
            // is bounded too.
            val bytes = resolver.openInputStream(uri)?.use { input ->
                val out = java.io.ByteArrayOutputStream()
                val buffer = ByteArray(64 * 1024)
                var total = 0L
                while (true) {
                    val n = input.read(buffer)
                    if (n < 0) {
                        break
                    }
                    total += n
                    if (total > maxBytes) {
                        promise.resolve(
                            Arguments.createMap().apply {
                                putString("name", name)
                                putDouble("size", total.toDouble())
                                putBoolean("tooLarge", true)
                            },
                        )
                        return
                    }
                    out.write(buffer, 0, n)
                }
                out.toByteArray()
            } ?: throw IllegalStateException("the picked file could not be opened")
            val mime = resolver.getType(uri) ?: mimeOf("", name)
            promise.resolve(
                Arguments.createMap().apply {
                    putString("name", name)
                    putString("contentType", mime)
                    putDouble("size", bytes.size.toDouble())
                    putString("base64", Base64.encodeToString(bytes, Base64.NO_WRAP))
                },
            )
        } catch (e: Exception) {
            // The message names the operation, never the bytes or the name.
            promise.reject(ERROR_CODE, "the picked file could not be read", e)
        }
    }

    /**
     * saveToDownloads writes the file to [SUBFOLDER] inside the shared
     * Downloads folder, so received files stay together, and
     * resolves with `{ name, uri }`: the name it was saved under — which can
     * differ from the one asked for, because Android adds " (1)" rather than
     * overwrite — and a `content://` URI [openSaved] can open it by.
     *
     * From Android 10 this goes through MediaStore and needs no permission.
     * Before that it is a plain file write, which needs WRITE_EXTERNAL_STORAGE;
     * the JavaScript side asks for it first on those versions.
     */
    @ReactMethod
    fun saveToDownloads(base64: String, filename: String, contentType: String, promise: Promise) {
        try {
            val bytes = Base64.decode(base64, Base64.DEFAULT)
            val mime = mimeOf(contentType, filename)
            val name = safeName(filename, mime)
            val context = reactApplicationContext
            val (savedName, uri) = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                saveWithMediaStore(context, bytes, name, mime)
            } else {
                saveLegacy(context, bytes, name, mime)
            }
            promise.resolve(
                Arguments.createMap().apply {
                    putString("name", savedName)
                    putString("uri", uri.toString())
                },
            )
        } catch (e: Exception) {
            // The message names the operation, never the bytes.
            promise.reject(ERROR_CODE, "the file could not be saved to Downloads", e)
        }
    }

    @RequiresApi(Build.VERSION_CODES.Q)
    private fun saveWithMediaStore(context: Context, bytes: ByteArray, name: String, mime: String): Pair<String, Uri> {
        val resolver = context.contentResolver
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, mime)
            // MediaStore creates the subfolder on first use.
            put(MediaStore.MediaColumns.RELATIVE_PATH, "${Environment.DIRECTORY_DOWNLOADS}/$SUBFOLDER")
            // Hidden from other apps until the bytes are all there.
            put(MediaStore.MediaColumns.IS_PENDING, 1)
        }
        val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            ?: throw IllegalStateException("MediaStore refused the insert")
        try {
            resolver.openOutputStream(uri)?.use { it.write(bytes) }
                ?: throw IllegalStateException("MediaStore gave no output stream")
            values.clear()
            values.put(MediaStore.MediaColumns.IS_PENDING, 0)
            resolver.update(uri, values, null, null)
        } catch (e: Exception) {
            // A half-written download is worse than none.
            resolver.delete(uri, null, null)
            throw e
        }
        resolver.query(uri, arrayOf(MediaStore.MediaColumns.DISPLAY_NAME), null, null, null)?.use {
            if (it.moveToFirst()) {
                return Pair(it.getString(0) ?: name, uri)
            }
        }
        return Pair(name, uri)
    }

    @Suppress("DEPRECATION")
    private fun saveLegacy(context: Context, bytes: ByteArray, name: String, mime: String): Pair<String, Uri> {
        val directory = File(
            Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS),
            SUBFOLDER,
        )
        if (!directory.exists() && !directory.mkdirs()) {
            throw IllegalStateException("the $SUBFOLDER folder in Downloads could not be created")
        }
        val file = uniqueFile(directory, name)
        file.outputStream().use { it.write(bytes) }
        // So the file shows up in the Files app without a reboot.
        MediaScannerConnection.scanFile(context, arrayOf(file.absolutePath), arrayOf(mime), null)
        // A file:// URI cannot be handed to another app, so the saved file is
        // reached through the provider's "downloads" path (res/xml/file_paths).
        return Pair(file.name, FileProvider.getUriForFile(context, "${context.packageName}.clips", file))
    }

    /** uniqueFile picks "name (1).ext", "name (2).ext", … rather than overwrite. */
    private fun uniqueFile(directory: File, name: String): File {
        var file = File(directory, name)
        if (!file.exists()) {
            return file
        }
        val dot = name.lastIndexOf('.')
        val stem = if (dot > 0) name.substring(0, dot) else name
        val ext = if (dot > 0) name.substring(dot) else ""
        var n = 1
        while (file.exists()) {
            file = File(directory, "$stem ($n)$ext")
            n++
        }
        return file
    }

    /**
     * open shows the file in another app, without saving it anywhere the user
     * would have to clean up. It rejects when no installed app opens the type,
     * so the user can save it instead.
     */
    @ReactMethod
    fun open(base64: String, filename: String, contentType: String, promise: Promise) {
        try {
            val mime = mimeOf(contentType, filename)
            val (file, uri) = stage(base64, filename, mime)
            val intent = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            try {
                reactApplicationContext.startActivity(intent)
            } catch (e: ActivityNotFoundException) {
                file.delete()
                promise.reject(ERROR_CODE, "no app on this device can open $mime files", e)
                return
            }
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "the file could not be opened", e)
        }
    }

    /**
     * share hands the file to Android's share sheet, so the user can send it
     * on to another app — a mail, a chat, a cloud drive. Like [open], nothing
     * is saved to Downloads: it is staged in the cache under the same rule.
     *
     * The chooser is always shown, even when one app is the default for the
     * type, because where a file goes next is a choice made each time.
     */
    @ReactMethod
    fun share(base64: String, filename: String, contentType: String, promise: Promise) {
        try {
            val mime = mimeOf(contentType, filename)
            val (file, uri) = stage(base64, filename, mime)
            val send = Intent(Intent.ACTION_SEND).apply {
                type = mime
                putExtra(Intent.EXTRA_STREAM, uri)
                // The grant travels with the ClipData: EXTRA_STREAM alone is
                // not covered by FLAG_GRANT_READ_URI_PERMISSION through a chooser.
                clipData = ClipData.newRawUri(file.name, uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            val chooser = Intent.createChooser(send, null).apply {
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            try {
                reactApplicationContext.startActivity(chooser)
            } catch (e: ActivityNotFoundException) {
                file.delete()
                promise.reject(ERROR_CODE, "no app on this device can receive this file", e)
                return
            }
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "the file could not be shared", e)
        }
    }

    /**
     * stage writes the file to this app's cache, replacing whatever was staged
     * before, and returns it with the FileProvider URI another app reads it by.
     * One staged file at a time keeps the cache from growing with every open.
     */
    private fun stage(base64: String, filename: String, mime: String): Pair<File, Uri> {
        val context = reactApplicationContext
        val directory = File(context.cacheDir, OPEN_DIRECTORY)
        directory.listFiles()?.forEach { it.delete() }
        if (!directory.exists() && !directory.mkdirs()) {
            throw IllegalStateException("the file staging directory could not be created")
        }
        // The real name is kept, because the receiving app shows it — and
        // some pick how to read the file from its extension.
        val file = File(directory, safeName(filename, mime))
        file.outputStream().use { it.write(Base64.decode(base64, Base64.DEFAULT)) }
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.clips", file)
        return file to uri
    }

    /**
     * openSaved opens a file [saveToDownloads] saved, by the URI it returned,
     * in whichever app opens its type. Only `content://` URIs are accepted:
     * this opens what this app saved, not arbitrary paths.
     */
    @ReactMethod
    fun openSaved(uri: String, contentType: String, filename: String, promise: Promise) {
        try {
            val parsed = Uri.parse(uri)
            if (parsed.scheme != "content") {
                promise.reject(ERROR_CODE, "only a saved file can be opened this way")
                return
            }
            val mime = mimeOf(contentType, filename)
            val intent = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(parsed, mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            try {
                reactApplicationContext.startActivity(intent)
            } catch (e: ActivityNotFoundException) {
                promise.reject(ERROR_CODE, "no app on this device can open $mime files", e)
                return
            }
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject(ERROR_CODE, "the saved file could not be opened", e)
        }
    }

    companion object {
        const val NAME = "TppFiles"

        /** SUBFOLDER is where saved files go, inside Downloads. */
        const val SUBFOLDER = "TwoPlacePaste"
        private const val OPEN_DIRECTORY = "open"
        private const val PICK_REQUEST = 7301
        private const val ERROR_CODE = "tpp_files"
        private const val OCTET_STREAM = "application/octet-stream"

        /**
         * mimeOf is the bare media type, without parameters like "; charset=".
         * An unknown one is guessed from the extension, because an opening app
         * is chosen by type and "application/octet-stream" matches almost none.
         */
        internal fun mimeOf(contentType: String, filename: String): String {
            val bare = contentType.substringBefore(';').trim().lowercase()
            if (bare.isNotEmpty() && bare != OCTET_STREAM) {
                return bare
            }
            val ext = filename.substringAfterLast('.', "").lowercase()
            return MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: OCTET_STREAM
        }

        /**
         * safeName makes a filename from another device safe to write here.
         * It is display text, never a path: separators and control characters
         * go, and a missing name gets one with an extension that fits the type.
         */
        internal fun safeName(filename: String, mime: String): String {
            val cleaned = filename
                .substringAfterLast('/')
                .substringAfterLast('\\')
                .filter { it >= ' ' && it != ':' && it != '*' && it != '?' && it != '"' && it != '<' && it != '>' && it != '|' }
                .trim()
                .trimStart('.')
                .take(120)
            if (cleaned.isNotEmpty()) {
                return cleaned
            }
            val ext = MimeTypeMap.getSingleton().getExtensionFromMimeType(mime)
            return if (ext == null) "TwoPlacePaste file" else "TwoPlacePaste file.$ext"
        }
    }
}
