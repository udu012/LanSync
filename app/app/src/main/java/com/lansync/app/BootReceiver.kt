package com.lansync.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** 开机后把定时同步重新挂上（系统重启会清掉 WorkManager 之外的状态）。 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val p = Prefs.load(ctx)
        if (p.paired && p.auto) {
            Scheduler.enable(ctx)
        }
    }
}
