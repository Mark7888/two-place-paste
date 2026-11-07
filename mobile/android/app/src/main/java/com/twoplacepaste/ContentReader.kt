package com.twoplacepaste

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import android.util.Base64
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.RandomAccessFile

/**
 * Reads a `content://` URI another app handed this one: a file the user
 * picked in the system's picker ([FileModule.pickFile]) or one they shared
 * to this app ([ShareModule]). Both reach the bytes through the one-off read
 * grant that came with the URI, and both go through here so that the size
 * limit is enforced the same way on either path.
 *
 * A file can be tens of megabytes, and the Java heap this app gets is a few
 * hundred. Reading one whole and handing it to JavaScript as one base64
 * string costs several times its size at once, which is what made a large
 * share crash the app. So a share is [stage]d — copied, streaming, into the
 * app's cache — and handed over in [chunk]s instead.
 */
internal object ContentReader {

    /** Meta is what a provider says about a URI before it is read. */
    data class Meta(val name: String, val size: Long)

    /**
     * meta asks the provider for the URI's display name and size. Either can
     * be missing — the size most often — and is then "" or -1.
     */
    fun meta(context: Context, uri: Uri): Meta {
        var name = ""
        var size = -1L
        context.contentResolver
            .query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)
            ?.use {
                if (it.moveToFirst()) {
                    val nameColumn = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    val sizeColumn = it.getColumnIndex(OpenableColumns.SIZE)
                    if (nameColumn >= 0) {
                        name = it.getString(nameColumn) ?: ""
                    }
                    if (sizeColumn >= 0 && !it.isNull(sizeColumn)) {
                        size = it.getLong(sizeColumn)
                    }
                }
            }
        return Meta(name, size)
    }

    /**
     * read resolves [promise] with the file as `{ name, contentType, size,
     * base64 }`, or with `{ name, size, tooLarge: true }` and no bytes when it
     * is over [maxBytes]. It blocks, so it is called off the main thread.
     *
     * [name] and [contentType] are used when given; otherwise they are asked
     * of the provider.
     */
    fun read(
        context: Context,
        uri: Uri,
        maxBytes: Long,
        promise: Promise,
        name: String? = null,
        contentType: String? = null,
    ) {
        try {
            val resolver = context.contentResolver
            val meta = meta(context, uri)
            val displayName = name ?: meta.name
            if (meta.size > maxBytes) {
                promise.resolve(tooLarge(displayName, meta.size))
                return
            }
            // The size a provider reports can be missing or wrong, so the read
            // is bounded too.
            val out = ByteArrayOutputStream(if (meta.size in 1..maxBytes) meta.size.toInt() else 64 * 1024)
            val input = resolver.openInputStream(uri)
                ?: throw IllegalStateException("the file could not be opened")
            input.use {
                val buffer = ByteArray(64 * 1024)
                var total = 0L
                while (true) {
                    val n = it.read(buffer)
                    if (n < 0) {
                        break
                    }
                    total += n
                    if (total > maxBytes) {
                        promise.resolve(tooLarge(displayName, total))
                        return
                    }
                    out.write(buffer, 0, n)
                }
            }
            val bytes = out.toByteArray()
            val mime = contentType ?: resolver.getType(uri) ?: FileModule.mimeOf("", displayName)
            promise.resolve(
                Arguments.createMap().apply {
                    putString("name", displayName)
                    putString("contentType", mime)
                    putDouble("size", bytes.size.toDouble())
                    putString("base64", Base64.encodeToString(bytes, Base64.NO_WRAP))
                },
            )
        } catch (e: OutOfMemoryError) {
            // Rejected rather than left to crash the app.
            promise.reject(ERROR_CODE, "the file is too large to read on this device", e)
        } catch (e: Exception) {
            // The message names the operation, never the bytes or the name.
            promise.reject(ERROR_CODE, "the file could not be read", e)
        }
    }

    /**
     * stage copies [uri] into [dest], streaming, and returns the number of
     * bytes copied — or null when the file is over [maxBytes], in which case
     * nothing is left behind. It blocks, so it is called off the main thread.
     * Only a buffer's worth of the file is in memory at any time.
     */
    fun stage(context: Context, uri: Uri, maxBytes: Long, dest: File): Long? {
        val input = context.contentResolver.openInputStream(uri)
            ?: throw IllegalStateException("the file could not be opened")
        dest.parentFile?.mkdirs()
        var total = 0L
        var fits = true
        try {
            input.use { inp ->
                dest.outputStream().use { out ->
                    val buffer = ByteArray(64 * 1024)
                    while (true) {
                        val n = inp.read(buffer)
                        if (n < 0) {
                            break
                        }
                        total += n
                        if (total > maxBytes) {
                            fits = false
                            break
                        }
                        out.write(buffer, 0, n)
                    }
                }
            }
        } catch (e: Exception) {
            dest.delete()
            throw e
        }
        if (!fits) {
            dest.delete()
            return null
        }
        return total
    }

    /** chunk returns [length] bytes of [file] from [offset], as base64. */
    fun chunk(file: File, offset: Long, length: Int): String {
        val bytes = ByteArray(length)
        RandomAccessFile(file, "r").use {
            it.seek(offset)
            it.readFully(bytes)
        }
        return Base64.encodeToString(bytes, Base64.NO_WRAP)
    }

    private fun tooLarge(name: String, size: Long) =
        Arguments.createMap().apply {
            putString("name", name)
            putDouble("size", size.toDouble())
            putBoolean("tooLarge", true)
        }

    private const val ERROR_CODE = "tpp_read"
}
