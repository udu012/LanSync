package com.lansync.app

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.database.ContentObserver
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.provider.MediaStore

/**
 * 常驻守护：盯着系统媒体库，一有新照片/视频就自动备份。
 *
 * 光靠 WorkManager 的 15 分钟周期任务不够——那是"最多 15 分钟一次"，
 * 系统省电时还会把周期任务往后推，拍完照半小时电脑上还没出现是常态。
 * 这里注册 ContentObserver 收"媒体库变了"的系统通知（相机存完照片会发），
 * 等几秒（连拍会连续触发很多次）再跑一次同步，通常 10 秒内电脑上就能看到。
 */
class WatchService : Service() {

    companion object {
        fun start(ctx: Context) {
            val i = Intent(ctx, WatchService::class.java)
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    ctx.startForegroundService(i)
                } else {
                    ctx.startService(i)
                }
            } catch (e: Exception) {
                // Android 12+ 从后台启动前台服务可能被系统拒绝，忽略即可，
                // 还有 WorkManager 的定时任务兜底。
            }
        }

        fun stop(ctx: Context) {
            try {
                ctx.stopService(Intent(ctx, WatchService::class.java))
            } catch (e: Exception) {
            }
        }
    }

    private var thread: HandlerThread? = null
    private var handler: Handler? = null
    private var observer: ContentObserver? = null

    /** 正在同步时不再重入，记下来等这一轮结束再补一轮。 */
    @Volatile
    private var busy = false
    @Volatile
    private var pending = false

    private val debounce = Runnable { runSync() }

    override fun onCreate() {
        super.onCreate()

        val initial = Notifications.watching(this, "正在待命")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(Notifications.ID_WATCH, initial, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(Notifications.ID_WATCH, initial)
        }

        val t = HandlerThread("lansync-watch").also { it.start() }
        thread = t
        val h = Handler(t.looper)
        handler = h

        val obs = object : ContentObserver(h) {
            override fun onChange(selfChange: Boolean) {
                schedule()
            }
        }
        observer = obs
        try {
            contentResolver.registerContentObserver(
                MediaStore.Files.getContentUri("external"), true, obs
            )
        } catch (e: Exception) {
            // 没给媒体权限时注册会失败，服务照常活着，等权限到位后用户点一次同步
        }
    }

    /** 连拍会连着触发十几次 onChange，统一攒 6 秒，只跑最后一轮。 */
    private fun schedule() {
        val h = handler ?: return
        h.removeCallbacks(debounce)
        h.postDelayed(debounce, 6000)
    }

    private fun runSync() {
        val p = Prefs.load(this)
        if (!p.paired || !p.auto) return
        if (busy) {
            pending = true
            return
        }
        busy = true
        try {
            do {
                pending = false
                if (!Api(Prefs.load(this)).ping()) return // 不在家/电脑没开，静默退出
                val r = SyncEngine.sync(this)
                if (r.uploaded > 0 || !r.ok) {
                    Notifications.result(this, r.ok, r.message)
                }
                updateIdle(r.message)
            } while (pending)
        } catch (e: Exception) {
            // 网络抖动之类的异常不该让守护进程挂掉
        } finally {
            busy = false
        }
    }

    private fun updateIdle(last: String) {
        try {
            val n = Notifications.watching(this, last.ifEmpty { "正在待命" })
            androidx.core.app.NotificationManagerCompat.from(this)
                .notify(Notifications.ID_WATCH, n)
        } catch (e: SecurityException) {
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // 被系统杀掉后自动回来，不用用户再进 App
        return START_STICKY
    }

    override fun onDestroy() {
        observer?.let {
            try {
                contentResolver.unregisterContentObserver(it)
            } catch (e: Exception) {
            }
        }
        observer = null
        handler?.removeCallbacksAndMessages(null)
        handler = null
        thread?.quitSafely()
        thread = null
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null
}
