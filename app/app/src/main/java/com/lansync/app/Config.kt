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
    val lastResult: String = ""
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
            lastResult = p.getString("last_result", "").orEmpty()
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
            .apply()
    }

    fun clear(c: Context) = sp(c).edit().clear().apply()
}
