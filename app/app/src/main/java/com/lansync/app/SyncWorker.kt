package com.lansync.app

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

/** 后台定时同步：系统会在连上 Wi-Fi 时择机执行，通常 15 分钟内完成一次。 */
class SyncWorker(ctx: Context, params: WorkerParameters) : Worker(ctx, params) {
    override fun doWork(): Result {
        val prefs = Prefs.load(applicationContext)
        if (!prefs.paired || !prefs.auto) return Result.success()
        if (!Api(prefs).ping()) return Result.success() // 不在家里，下次再说

        val r = SyncEngine.sync(applicationContext)
        if (r.uploaded > 0 || !r.ok) {
            Notifications.result(applicationContext, r.ok, r.message)
        }
        return Result.success()
    }
}

object Scheduler {

    private const val WORK = "lansync-periodic"

    fun enable(ctx: Context) {
        val constraints = Constraints.Builder()
            // 不能写 UNMETERED（只在 Wi-Fi 下跑）：手机开热点时 Wi-Fi 是关着的，
            // 系统判定当前是蜂窝网络且计费，这个条件永远不满足，任务根本不会启动。
            // 改成 CONNECTED，再靠 doWork 里的 ping 判断电脑在不在同一局域网——
            // ping 不通就直接跳过，不会走移动数据传文件，一分流量都不花。
            .setRequiredNetworkType(NetworkType.CONNECTED)
            .build()

        val req = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
            .setConstraints(constraints)
            .build()

        WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(
            WORK, ExistingPeriodicWorkPolicy.KEEP, req
        )
    }

    fun disable(ctx: Context) {
        WorkManager.getInstance(ctx).cancelUniqueWork(WORK)
    }
}
