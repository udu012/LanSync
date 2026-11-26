package com.lansync.app

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import kotlin.concurrent.thread

/** 手动「立即同步」用的一次性前台服务，带进度通知。 */
class SyncService : Service() {

    companion object {
        fun start(ctx: Context) {
            val i = Intent(ctx, SyncService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                ctx.startForegroundService(i)
            } else {
                ctx.startService(i)
            }
        }
    }

    @Volatile
    private var running = false

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val initial = Notifications.serviceProgress(this, "LanSync 同步中", "正在连接电脑…", -1)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(Notifications.ID_SERVICE, initial, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(Notifications.ID_SERVICE, initial)
        }

        if (running) return START_NOT_STICKY
        running = true

        thread(name = "lansync-sync") {
            val result = try {
                SyncEngine.sync(applicationContext) { done, total, name ->
                    val pct = if (total > 0) done * 100 / total else 0
                    val n = Notifications.serviceProgress(
                        this, "LanSync 同步中",
                        "(${done + 1}/$total) $name", pct
                    )
                    try {
                        androidx.core.app.NotificationManagerCompat.from(this)
                            .notify(Notifications.ID_SERVICE, n)
                    } catch (e: SecurityException) {
                    }
                }
            } catch (e: Exception) {
                SyncEngine.Result(false, 0, 0, 0, "同步出错：${e.message}")
            }

            Notifications.result(applicationContext, result.ok, result.message)
            running = false

            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
                stopForeground(STOP_FOREGROUND_REMOVE)
            } else {
                @Suppress("DEPRECATION")
                stopForeground(true)
            }
            stopSelf()
        }

        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null
}
