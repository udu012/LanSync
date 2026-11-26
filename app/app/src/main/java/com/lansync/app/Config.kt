package com.lansync.app

import android.content.Context
import android.content.SharedPreferences

/** 配对信息与偏好设置，存在 SharedPreferences 里。 */
data class PrefsData(
    val host: String = "",
    val port: Int = 8765,
    val token: String = "",
    val auto: Boolean = true,
    val lastSyncAt: Long = 0L,
    val lastResult: String = "",
    /**
     * 用户关掉的相册（手机上的相对目录，如 "Pictures/WeChat"）。
     *
     * 存"排除名单"而不是"包含名单"：以后手机上新装了 App、多出一个相册，
     * 默认就该备份，不需要用户再回来勾一次。空集合 = 全部备份。
     */
    val excluded: Set<String> = emptySet()
) {
    val paired: Boolean get() = host.isNotEmpty() && token.isNotEmpty()
    val baseUrl: String get() = "http://$host:$port"
}

object Prefs {
    private const val FILE = "lansync_prefs"
    private fun sp(c: Context): SharedPreferences = c.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    fun load(c: Context): PrefsData {
        val p = sp(c)
        return PrefsData(
            host = p.getString("host", "").orEmpty(),
            port = p.getInt("port", 8765),
            token = p.getString("token", "").orEmpty(),
            auto = p.getBoolean("auto", true),
            lastSyncAt = p.getLong("last_sync", 0L),
            lastResult = p.getString("last_result", "").orEmpty(),
            excluded = p.getStringSet("excluded", null)?.toHashSet() ?: emptySet()
        )
    }

    fun save(c: Context, d: PrefsData) {
        sp(c).edit()
            .putString("host", d.host)
            .putInt("port", d.port)
            .putString("token", d.token)
            .putBoolean("auto", d.auto)
            .putLong("last_sync", d.lastSyncAt)
            .putString("last_result", d.lastResult)
            .putStringSet("excluded", d.excluded)
            .apply()
    }

    fun clear(c: Context) = sp(c).edit().clear().apply()
}
