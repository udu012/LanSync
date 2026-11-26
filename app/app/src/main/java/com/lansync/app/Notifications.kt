package com.lansync.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat

object Notifications {

    const val CH_RESULT = "sync_result"
    const val CH_SERVICE = "sync_service"
    const val ID_SERVICE = 1001
    const val ID_RESULT = 1002

    fun ensureChannels(ctx: Context) {
        val nm = ctx.getSystemService(NotificationManager::class.java) ?: return
        nm.createNotificationChannel(
            NotificationChannel(CH_RESULT, "同步结果", NotificationManager.IMPORTANCE_DEFAULT).apply {
                description = "每次备份完成后通知你"
            }
        )
        nm.createNotificationChannel(
            NotificationChannel(CH_SERVICE, "同步进行中", NotificationManager.IMPORTANCE_LOW).apply {
                description = "手动同步时的进度提示"
                setShowBadge(false)
            }
        )
    }

    private fun openApp(ctx: Context): PendingIntent {
        val i = Intent(ctx, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        return PendingIntent.getActivity(
            ctx, 0, i,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    fun serviceProgress(ctx: Context, title: String, text: String, progress: Int): Notification {
        return NotificationCompat.Builder(ctx, CH_SERVICE)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle(title)
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setProgress(100, progress.coerceIn(0, 100), progress < 0)
            .setContentIntent(openApp(ctx))
            .build()
    }

    fun result(ctx: Context, ok: Boolean, message: String) {
        ensureChannels(ctx)
        val n = NotificationCompat.Builder(ctx, CH_RESULT)
            .setSmallIcon(if (ok) android.R.drawable.stat_sys_download_done else android.R.drawable.stat_notify_error)
            .setContentTitle(if (ok) "LanSync · 备份完成" else "LanSync · 同步未完成")
            .setContentText(message)
            .setStyle(NotificationCompat.BigTextStyle().bigText(message))
            .setAutoCancel(true)
            .setContentIntent(openApp(ctx))
            .build()
        try {
            NotificationManagerCompat.from(ctx).notify(ID_RESULT, n)
        } catch (e: SecurityException) {
            // 用户没给通知权限，忽略即可
        }
    }
}
