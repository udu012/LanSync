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
    const val CH_WATCH = "watch_service"
    const val ID_SERVICE = 1001
    const val ID_RESULT = 1002
    const val ID_WATCH = 1003

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
        // 常驻静默通知：不响、不弹、不显示角标，只是告诉系统「这个服务还在干活」，
        // 顺便让用户随时能点进来看当前状态。
        nm.createNotificationChannel(
            NotificationChannel(CH_WATCH, "自动备份守护", NotificationManager.IMPORTANCE_LOW).apply {
                description = "发现新照片就自动备份，常驻但完全静音"
                setShowBadge(false)
                lockscreenVisibility = Notification.VISIBILITY_SECRET
            }
        )
    }

    /** 守护服务的前台通知：新版 Android 要求前台服务必须挂一条可见通知。 */
    fun watching(ctx: Context, text: String): Notification {
        return NotificationCompat.Builder(ctx, CH_WATCH)
            .setSmallIcon(android.R.drawable.stat_notify_sync_noanim)
            .setContentTitle("LanSync 自动备份已开启")
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .setContentIntent(openApp(ctx))
            .build()
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
