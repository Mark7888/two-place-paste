package com.twoplacepaste

import android.app.PendingIntent
import android.content.Intent
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService

/**
 * The quick-settings tile (SPEC §7.1).
 *
 * This is the app's primary sync mechanism, and it exists because of one
 * platform rule: since Android 10, an app that is not focused and is not the
 * default IME cannot read the clipboard. Tapping the tile brings a window of
 * this app forward, which is a legal moment to read it; the JavaScript side
 * then performs exactly one sync and calls back into [TileModule], which
 * renders the outcome here and shows a toast.
 *
 * The window is [SyncOverlayActivity] — a transparent panel over whatever the
 * user is doing — and not [MainActivity]. Focus is all the platform rule asks
 * for, and taking the whole screen to get it made a one-second sync cost the
 * user their place in another app.
 *
 * What this service deliberately does not do: hold a socket, read the
 * clipboard itself, or run any work of its own. It starts the app and waits to
 * be told how it went.
 */
class SyncTileService : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        render(TileModule.lastOutcome)
    }

    override fun onClick() {
        super.onClick()
        setState(Tile.STATE_ACTIVE, getString(R.string.tile_label))

        // The request is recorded before the activity starts, so a cold start
        // finds it waiting: the app claims it during startup rather than
        // needing the tap to survive process creation as an Intent extra.
        TileModule.requestSync(applicationContext)

        val intent = Intent(this, SyncOverlayActivity::class.java).apply {
            addFlags(
                Intent.FLAG_ACTIVITY_NEW_TASK or
                    Intent.FLAG_ACTIVITY_SINGLE_TOP or
                    // A second tap while the panel is up reuses it rather than
                    // stacking another one behind it.
                    Intent.FLAG_ACTIVITY_CLEAR_TOP,
            )
            action = ACTION_TILE_SYNC
        }
        // startActivityAndCollapse is the only way to bring an app forward from
        // the shade, and from Android 14 it must be handed a PendingIntent.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startActivityAndCollapse(
                PendingIntent.getActivity(
                    this,
                    0,
                    intent,
                    PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
                ),
            )
        } else {
            @Suppress("DEPRECATION")
            startActivityAndCollapse(intent)
        }
    }

    /** render shows the last sync's outcome on the tile itself. */
    private fun render(outcome: TileModule.Outcome?) {
        when (outcome) {
            null -> setState(Tile.STATE_INACTIVE, getString(R.string.tile_label))
            else -> setState(
                if (outcome.ok) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE,
                outcome.label,
            )
        }
    }

    private fun setState(state: Int, label: String) {
        val tile = qsTile ?: return
        tile.state = state
        tile.label = getString(R.string.tile_label)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            // The subtitle is the only place the outcome fits. It says what
            // happened — never what was copied.
            tile.subtitle = label
        }
        tile.updateTile()
    }

    companion object {
        /** ACTION_TILE_SYNC marks the activity start as a tile tap rather than a launcher tap. */
        const val ACTION_TILE_SYNC = "com.twoplacepaste.action.TILE_SYNC"
    }
}
