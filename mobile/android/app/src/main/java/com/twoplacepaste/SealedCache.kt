package com.twoplacepaste

import android.content.Context
import java.io.File
import org.json.JSONObject

/**
 * Containers downloaded natively, kept still sealed so that a preview, a save
 * and an open of the same file within the hour are one download
 * (`DownloadSink.cached` / `keep` in the client core).
 *
 * Only sealed containers are kept — never a decrypted body — for up to an
 * hour (the client sets each one's time, never past the entry's own expiry),
 * and at most [MAX_BYTES] in all, the oldest going first. They live in the
 * app's cache directory, in a folder no provider serves, beside an index of
 * what each one is. Unlike the staged copies of an upload, they outlast the
 * process: the hour is what bounds them.
 *
 * A file is addressed by the token it was staged under, which moves with it
 * into the cache, so reading and opening it work the same either way.
 */
internal object SealedCache {

    private const val DIRECTORY = "sealed"
    private const val INDEX = "index.json"

    /** MAX_BYTES bounds the cache: four entries at the relay's default 50 MiB cap, as the in-memory cache. */
    private const val MAX_BYTES = 200L shl 20

    /** KEY is what a cache key must look like: "entry:" or "store:" and an id. */
    private val KEY = Regex("^(entry|store):[A-Za-z0-9_-]{1,128}$")

    /** Kept is one container: its staged token, its size, what was kept with it, and until when. */
    data class Kept(val token: String, val size: Long, val info: String, val until: Long)

    private var index: MutableMap<String, Kept>? = null

    fun directory(context: Context) = File(context.cacheDir, DIRECTORY)

    /** lookup returns what is kept under [key], or null when nothing is or it has expired. */
    @Synchronized
    fun lookup(context: Context, key: String, now: Long): Kept? {
        val entries = load(context)
        prune(context, entries, now)
        return entries[key]
    }

    /**
     * keep moves [staged] — a container staged under [token] — into the cache
     * under [key] until [until], making room first.
     */
    @Synchronized
    fun keep(context: Context, key: String, token: String, staged: File, info: String, until: Long, now: Long) {
        require(KEY.matches(key)) { "not a cache key" }
        if (until <= now || !staged.exists()) {
            return
        }
        val size = staged.length()
        if (size > MAX_BYTES) {
            return
        }
        val entries = load(context)
        entries.remove(key)?.let { File(directory(context), it.token).delete() }
        prune(context, entries, now)
        // The oldest go first, until this one fits.
        val byAge = entries.entries.sortedBy { it.value.until }.toMutableList()
        var total = entries.values.sumOf { it.size }
        while (total + size > MAX_BYTES && byAge.isNotEmpty()) {
            val oldest = byAge.removeAt(0)
            entries.remove(oldest.key)
            File(directory(context), oldest.value.token).delete()
            total -= oldest.value.size
        }
        val dir = directory(context)
        dir.mkdirs()
        val target = File(dir, token)
        if (!staged.renameTo(target)) {
            return
        }
        entries[key] = Kept(token, size, info, until)
        save(context, entries)
    }

    /** forget drops what is kept under [key]. */
    @Synchronized
    fun forget(context: Context, key: String) {
        val entries = load(context)
        entries.remove(key)?.let {
            File(directory(context), it.token).delete()
            save(context, entries)
        }
    }

    /** clear drops everything kept: the device left the group, or switched to another. */
    @Synchronized
    fun clear(context: Context) {
        directory(context).listFiles()?.forEach { it.delete() }
        index = HashMap()
    }

    /** prune drops what has expired, and what has lost its file; files the index does not know go too. */
    private fun prune(context: Context, entries: MutableMap<String, Kept>, now: Long) {
        val dir = directory(context)
        var changed = false
        val iterator = entries.entries.iterator()
        while (iterator.hasNext()) {
            val e = iterator.next()
            if (now >= e.value.until || !File(dir, e.value.token).exists()) {
                File(dir, e.value.token).delete()
                iterator.remove()
                changed = true
            }
        }
        val known = entries.values.map { it.token }.toSet() + INDEX
        dir.listFiles()?.forEach { if (it.name !in known) it.delete() }
        if (changed) {
            save(context, entries)
        }
    }

    private fun load(context: Context): MutableMap<String, Kept> {
        index?.let { return it }
        val entries = HashMap<String, Kept>()
        try {
            val file = File(directory(context), INDEX)
            if (file.exists()) {
                val json = JSONObject(file.readText())
                for (key in json.keys()) {
                    val o = json.getJSONObject(key)
                    entries[key] = Kept(o.getString("token"), o.getLong("size"), o.getString("info"), o.getLong("until"))
                }
            }
        } catch (e: Exception) {
            // An index that cannot be read is an empty cache; prune clears the files.
            entries.clear()
        }
        index = entries
        return entries
    }

    private fun save(context: Context, entries: Map<String, Kept>) {
        val json = JSONObject()
        for ((key, e) in entries) {
            json.put(
                key,
                JSONObject().put("token", e.token).put("size", e.size).put("info", e.info).put("until", e.until),
            )
        }
        val dir = directory(context)
        dir.mkdirs()
        // Written aside and moved, so a crash leaves the old index or the new.
        val tmp = File(dir, "$INDEX.tmp")
        tmp.writeText(json.toString())
        tmp.renameTo(File(dir, INDEX))
    }
}
