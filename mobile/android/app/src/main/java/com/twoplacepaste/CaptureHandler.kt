package com.twoplacepaste

import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.WritableMap
import com.facebook.react.modules.websocket.WebSocketModule
import java.io.File
import java.io.RandomAccessFile
import java.util.concurrent.ConcurrentHashMap
import okio.ByteString
import okio.ByteString.Companion.toByteString

/**
 * Sees every frame React Native's WebSocket receives on one socket, and
 * captures the range replies JavaScript armed it for ([ShareModule.armCapture]).
 *
 * A frame nothing is armed for is handed to JavaScript exactly as React
 * Native's own module would hand it — text as text, binary as base64 — so
 * installing this changes nothing else on the socket. An armed reply has its
 * `data` field written to the staged download file and is handed on without
 * it ([RangeCapture]), so the bytes of a large download never cross into
 * JavaScript. Anything unexpected — a frame that does not parse, an error
 * instead of the reply, a file that cannot be written — goes to JavaScript
 * whole, and the capture resolves with null, so JavaScript handles it as it
 * always did.
 *
 * Frames arrive on OkHttp's reader thread, one at a time.
 */
class CaptureHandler : WebSocketModule.ContentHandler {

    /** Arm is one reply to capture: its type, and where its data goes. */
    class Arm(val responseType: Long, val file: File, val offset: Long, val promise: Promise)

    private val arms = ConcurrentHashMap<String, Arm>()

    fun arm(envelopeId: String, arm: Arm) {
        arms.put(envelopeId, arm)?.promise?.resolve(null)
    }

    fun release(envelopeId: String) {
        arms.remove(envelopeId)?.promise?.resolve(null)
    }

    fun releaseAll() {
        for (id in arms.keys.toList()) {
            release(id)
        }
    }

    override fun onMessage(text: String, params: WritableMap) {
        params.putString("data", text)
    }

    override fun onMessage(byteString: ByteString, params: WritableMap) {
        if (arms.isEmpty()) {
            params.putString("data", byteString.base64())
            return
        }
        val bytes = byteString.toByteArray()
        val env = RangeCapture.envelope(bytes)
        val arm = env?.let { arms.remove(it.id) }
        if (env == null || arm == null) {
            params.putString("data", byteString.base64())
            return
        }
        val split = if (env.type == arm.responseType) RangeCapture.split(bytes, env, DATA_FIELD) else null
        if (split == null) {
            arm.promise.resolve(null)
            params.putString("data", byteString.base64())
            return
        }
        try {
            RandomAccessFile(arm.file, "rw").use {
                it.seek(arm.offset)
                it.write(bytes, split.dataOffset, split.dataLength)
            }
        } catch (e: Throwable) {
            // JavaScript gets the whole frame and writes the range itself.
            arm.promise.resolve(null)
            params.putString("data", byteString.base64())
            return
        }
        params.putString("data", split.frame.toByteString().base64())
        arm.promise.resolve(split.dataLength.toDouble())
    }

    private companion object {
        /** DATA_FIELD is `data` in both range replies, EntryFetchChunkResponse and StoreFetchChunkResponse. */
        const val DATA_FIELD = 3
    }
}
