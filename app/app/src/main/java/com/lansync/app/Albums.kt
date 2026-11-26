package com.lansync.app

import android.content.Context
import java.util.Locale

/** 手机上的一个「相册」，其实就是 MediaStore 里的一个相对目录。 */
data class Album(val dir: String, val count: Int, val bytes: Long) {

    /** 列表主标题：常见目录给中文别名，其它的取路径最后一段。 */
    val title: String
        get() = Albums.aliasOf(dir) ?: dir.substringAfterLast('/').ifEmpty { dir }

    /** 列表副标题：完整路径 + 数量体积。同名相册（比如两个「微信」）靠它区分。 */
    val subtitle: String
        get() = "$dir · $count 个 · ${Albums.sizeText(bytes)}"
}

/**
 * 「备份范围」的数据来源。
 *
 * 只存排除名单：手机上新装一个 App、多出一个相册，默认就该被备份，
 * 用户不需要回来再勾一次。空名单 = 全部备份，跟老版本行为一致。
 */
object Albums {

    /** 常见的相册目录给个看得懂的名字，其余沿用路径本身。 */
    private val ALIAS = mapOf(
        "DCIM/Camera" to "相机",
        "DCIM/Screenshots" to "截屏",
        "DCIM/ScreenRecorder" to "录屏",
        "DCIM/Screenrecorder" to "录屏",
        "Pictures/Screenshots" to "截屏",
        "Pictures/ScreenRecorder" to "录屏",
        "Pictures/WeChat" to "微信",
        "Pictures/WeiXin" to "微信",
        "Pictures/wechat" to "微信",
        "DCIM/WeChat" to "微信",
        "Pictures/QQ" to "QQ",
        "Pictures/Tencent" to "腾讯",
        "Pictures/Weibo" to "微博",
        "Pictures/微博" to "微博",
        "Pictures/知乎" to "知乎",
        "Pictures/Browser" to "浏览器",
        "Pictures/Download" to "图片下载",
        "Download" to "下载",
        "DCIM" to "相机（根目录）",
        "Pictures" to "图片（根目录）",
        "Movies" to "视频",
        "Movies/WeChat" to "微信视频",
        "未分类" to "未分类"
    )

    fun aliasOf(dir: String): String? = ALIAS[dir]

    /** 全量统计，不受排除名单影响——勾选页要把关掉的相册也列出来。 */
    fun scan(ctx: Context): List<Album> {
        val items = MediaScanner.scan(ctx, applyFilter = false).items
        val map = HashMap<String, LongArray>()
        for (m in items) {
            val dir = m.path.substringBeforeLast('/', "")
            val key = if (dir.isEmpty()) "未分类" else dir
            val v = map[key] ?: LongArray(2).also { map[key] = it }
            v[0] = v[0] + 1L
            v[1] = v[1] + m.size
        }
        return map.map { (d, v) -> Album(d, v[0].toInt(), v[1]) }
            .sortedWith(compareByDescending<Album> { it.bytes }.thenBy { it.dir })
    }

    /**
     * 目录是不是被关掉了。
     *
     * 前缀也算：用户关掉 "Pictures/WeChat" 时，它下面新建的子目录
     * （微信偶尔会换名）一并跳过，不然关了个寂寞。
     */
    fun isExcluded(dir: String, excluded: Set<String>): Boolean {
        if (excluded.isEmpty()) return false
        for (e in excluded) {
            if (dir == e || dir.startsWith("$e/")) return true
        }
        return false
    }

    fun sizeText(b: Long): String = when {
        b >= 1L shl 30 -> String.format(Locale.US, "%.1f GB", b / 1073741824.0)
        b >= 1L shl 20 -> String.format(Locale.US, "%.1f MB", b / 1048576.0)
        b >= 1024L -> String.format(Locale.US, "%.0f KB", b / 1024.0)
        else -> "$b B"
    }
}
