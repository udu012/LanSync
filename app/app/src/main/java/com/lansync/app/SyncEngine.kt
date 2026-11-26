package com.lansync.app

import android.content.Context

/**
 * 同步引擎：扫描 → 核对 → 只传差异 → 回写状态。
 * 这一层不碰界面，Worker（后台定时）和服务（手动触发）都复用它。
 */
object SyncEngine {

    data class Result(
        val ok: Boolean,
        val uploaded: Int,
        val skipped: Int,
        val failed: Int,
        val message: String
    )

    fun sync(
        ctx: Context,
        onProgress: (done: Int, total: Int, name: String) -> Unit = { _, _, _ -> }
    ): Result {
        val prefs = Prefs.load(ctx)
        if (!prefs.paired) {
            return Result(false, 0, 0, 0, "还没配对，请扫描电脑上的二维码")
        }

        val api = Api(prefs)
        if (!api.ping()) {
            return Result(false, 0, 0, 0, "连不上电脑：请确认手机和电脑在同一 Wi-Fi，且电脑端 LanSync 正在运行")
        }

        val all = MediaScanner.scan(ctx)
        if (all.isEmpty()) {
            return remember(ctx, prefs, Result(true, 0, 0, 0, "没有找到照片或视频"))
        }

        val byPath = HashMap<String, MediaItem>(all.size)
        all.forEach { byPath[it.path] = it }

        val need = try {
            api.check(all.map { RemoteItem(it.path, it.size, it.mtime) })
        } catch (e: Exception) {
            return Result(false, 0, 0, 0, "核对失败：${e.message}")
        }

        val todo = need.mapNotNull { byPath[it] }
        val skipped = all.size - todo.size

        if (todo.isEmpty()) {
            return remember(ctx, prefs, Result(true, 0, skipped, 0, "全部一致，本次无需传输"))
        }

        var done = 0
        var failed = 0
        var bytes = 0L

        todo.forEachIndexed { index, m ->
            onProgress(index, todo.size, m.path.substringAfterLast('/'))
            val ok = ctx.contentResolver.openInputStream(m.uri)?.let { ins ->
                api.put(RemoteItem(m.path, m.size, m.mtime), ins) { }
            } ?: false

            if (ok) {
                done++
                bytes += m.size
            } else {
                failed++
            }
        }

        val msg = buildString {
            if (done > 0) append("已备份 $done 个文件")
            if (skipped > 0) {
                if (isNotEmpty()) append("，")
                append("$skipped 个已存在")
            }
            if (failed > 0) {
                if (isNotEmpty()) append("，")
                append("$failed 个失败")
            }
            if (isEmpty()) append("无事可做")
        }
        return remember(ctx, prefs, Result(failed == 0, done, skipped, failed, msg))
    }

    private fun remember(ctx: Context, prefs: PrefsData, r: Result): Result {
        Prefs.save(
            ctx,
            prefs.copy(lastSyncAt = System.currentTimeMillis(), lastResult = r.message)
        )
        return r
    }
}
