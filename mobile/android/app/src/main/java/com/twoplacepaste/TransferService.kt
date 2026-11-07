package com.twoplacepaste

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import com.facebook.react.HeadlessJsTaskService
import com.facebook.react.bridge.Arguments
import com.facebook.react.jstasks.HeadlessJsTaskConfig

/**
 * Runs the uploads the user started from the share panel after the panel has
 * closed, so they can go on using other apps meanwhile.
 *
 * Two things are needed for that, and this service is both:
 *
 *  - a **foreground service** with a notification, so Android neither freezes
 *    nor kills the process while a large file is encrypted and uploaded, and
 *    the user can see it going;
 *  - a **headless JS task**. React Native pauses JavaScript timers while no
 *    window of the app is on screen, unless a headless task is running, and
 *    the encryption and the upload yield through timers.
 *
 * The task is the JavaScript queue of uploads (`src/app/backgroundJobs.ts`);
 * it ends when the queue is empty, and the service stops with it. Starting the
 * service again while it runs starts a second task that simply waits for the
 * same queue.
 *
 * It never touches the clipboard: the clipboard rule of SPEC §7.1 is about
 * reading what the user copied, and this only carries what they chose to
 * share, after they chose where it goes.
 *
 * The notification says what is happening and how far along it is, never what
 * is being sent: it is visible on the lock screen.
 */
class TransferService : HeadlessJsTaskService() {

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Within the few seconds Android allows after startForegroundService.
        ServiceCompat.startForeground(
            this,
            NOTIFICATION_ID,
            TransferNotifications.progress(this, "Preparing…", "", 0, 0),
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
            } else {
                0
            },
        )
        super.onStartCommand(intent, flags, startId)
        // Not restarted after the process dies: the queue died with it.
        return START_NOT_STICKY
    }

    override fun getTaskConfig(intent: Intent?): HeadlessJsTaskConfig =
        // No timeout (0): an upload takes as long as the network does. Allowed
        // in the foreground, because the share panel starts it.
        HeadlessJsTaskConfig(TASK_KEY, Arguments.createMap(), 0, true)

    override fun onTimeout(startId: Int, fgsType: Int) {
        // Android 15+ caps data-sync services; an upload that long has
        // stalled anyway. JavaScript reports it as failed when it next runs.
        stopSelf()
    }

    override fun onDestroy() {
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    companion object {
        /** TASK_KEY is registered in `index.js` with `AppRegistry.registerHeadlessTask`. */
        const val TASK_KEY = "TppBackgroundTransfers"

        const val NOTIFICATION_ID = 7401

        /** start starts the service. Called while a window of the app is in the foreground. */
        fun start(context: Context) {
            androidx.core.content.ContextCompat.startForegroundService(
                context,
                Intent(context, TransferService::class.java),
            )
        }
    }
}

/** The notifications background uploads show: one ongoing, and one per outcome. */
internal object TransferNotifications {

    private const val CHANNEL_ID = "transfers"

    /** RESULT_TAG with the upload's id names its outcome notification, so each one gets its own. */
    private const val RESULT_TAG = "transfer-result"

    private fun channel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return
        }
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        if (manager.getNotificationChannel(CHANNEL_ID) == null) {
            manager.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "Uploads", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "Files and text you shared to TwoPlacePaste, while they upload."
                    setShowBadge(false)
                },
            )
        }
    }

    /** openApp is what tapping a notification does: bring the app forward. */
    private fun openApp(context: Context): PendingIntent? {
        val launch = context.packageManager.getLaunchIntentForPackage(context.packageName) ?: return null
        return PendingIntent.getActivity(context, 0, launch, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    /**
     * progress is the ongoing notification: [title] with a bar at
     * [done]/[total], or a moving bar when [total] is 0, and [detail] — "2 of
     * 3" — beside the percentage when there is more than one upload.
     */
    fun progress(context: Context, title: String, detail: String, done: Long, total: Long): Notification {
        channel(context)
        val builder = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_tile)
            .setContentTitle(title)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setCategory(NotificationCompat.CATEGORY_PROGRESS)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setContentIntent(openApp(context))
        val parts = mutableListOf<String>()
        if (total > 0) {
            val percent = (done * 100 / total).toInt().coerceIn(0, 100)
            builder.setProgress(100, percent, false)
            parts.add("$percent%")
        } else {
            builder.setProgress(0, 0, true)
        }
        if (detail.isNotEmpty()) {
            parts.add(detail)
        }
        if (parts.isNotEmpty()) {
            builder.setContentText(parts.joinToString(" · "))
        }
        return builder.build()
    }

    /** update replaces the ongoing notification, if notifications may be shown. */
    @android.annotation.SuppressLint("MissingPermission")
    fun update(context: Context, title: String, detail: String, done: Long, total: Long) {
        val manager = NotificationManagerCompat.from(context)
        if (manager.areNotificationsEnabled()) {
            manager.notify(TransferService.NOTIFICATION_ID, progress(context, title, detail, done, total))
        }
    }

    /**
     * result posts how upload [id] ended, as a notification of its own that
     * the user can swipe away, so two uploads in a row leave two outcomes.
     */
    @android.annotation.SuppressLint("MissingPermission")
    fun result(context: Context, id: Int, ok: Boolean, message: String) {
        channel(context)
        val manager = NotificationManagerCompat.from(context)
        if (!manager.areNotificationsEnabled()) {
            return
        }
        val notification = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_tile)
            .setContentTitle(if (ok) "Upload finished" else "Upload failed")
            .setContentText(message)
            .setStyle(NotificationCompat.BigTextStyle().bigText(message))
            .setAutoCancel(true)
            .setCategory(if (ok) NotificationCompat.CATEGORY_STATUS else NotificationCompat.CATEGORY_ERROR)
            .setContentIntent(openApp(context))
            .build()
        manager.notify(RESULT_TAG, id, notification)
    }
}
