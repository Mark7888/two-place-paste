package com.twoplacepaste

import android.content.ActivityNotFoundException
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.media.MediaScannerConnection
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.util.Base64
import android.webkit.MimeTypeMap
import androidx.annotation.RequiresApi
import androidx.core.content.FileProvider
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import java.io.File

/**
 * What happens to a received entry the Android clipboard cannot hold — a file
 * from a desktop, a PDF, an archive. The user picks one of two things:
 *
 *  - [saveToDownloads] keeps a copy in the shared Downloads folder;
 *  - [open] hands it to whichever app opens that type, and keeps nothing.
 *
 * Opening does not go through Downloads. The bytes are staged in this app's
 * cache and handed over as a FileProvider `content://` URI with a one-off read
 * grant, the same way a received image goes onto the clipboard. Each open
 * replaces the last one, so the cache never holds more than one file.
 */
class FileModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = NAME

    /**
     * saveToDownloads writes the file to the shared Downloads folder and
     * resolves with the name it was saved under — which can differ from the
     * one asked for, because Android adds " (1)" rather than overwrite.
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
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                promise.resolve(saveWithMediaStore(context, bytes, name, mime))
            } else {
                promise.resolve(saveLegacy(context, bytes, name, mime))
            }
        } catch (e: Exception) {
            // The message names the operation, never the bytes.
            promise.reject(ERROR_CODE, "the file could not be saved to Downloads", e)
        }
    }

    @RequiresApi(Build.VERSION_CODES.Q)
    private fun saveWithMediaStore(context: Context, bytes: ByteArray, name: String, mime: String): String {
        val resolver = context.contentResolver
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, mime)
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
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
                return it.getString(0) ?: name
            }
        }
        return name
    }

    @Suppress("DEPRECATION")
    private fun saveLegacy(context: Context, bytes: ByteArray, name: String, mime: String): String {
        val directory = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        if (!directory.exists() && !directory.mkdirs()) {
            throw IllegalStateException("the Downloads folder could not be created")
        }
        val file = uniqueFile(directory, name)
        file.outputStream().use { it.write(bytes) }
        // So the file shows up in the Files app without a reboot.
        MediaScannerConnection.scanFile(context, arrayOf(file.absolutePath), arrayOf(mime), null)
        return file.name
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
            val context = reactApplicationContext
            val mime = mimeOf(contentType, filename)
            val directory = File(context.cacheDir, OPEN_DIRECTORY)
            // One staged file at a time: the previous one is dropped first.
            directory.listFiles()?.forEach { it.delete() }
            if (!directory.exists() && !directory.mkdirs()) {
                promise.reject(ERROR_CODE, "the file staging directory could not be created")
                return
            }
            // The real name is kept, because the opening app shows it — and
            // some pick how to read the file from its extension.
            val file = File(directory, safeName(filename, mime))
            file.outputStream().use { it.write(Base64.decode(base64, Base64.DEFAULT)) }

            val uri = FileProvider.getUriForFile(context, "${context.packageName}.clips", file)
            val intent = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            try {
                context.startActivity(intent)
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

    companion object {
        const val NAME = "TppFiles"
        private const val OPEN_DIRECTORY = "open"
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
