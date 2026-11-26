package com.lansync.app

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okio.BufferedSink
import org.json.JSONArray
import org.json.JSONObject
import java.io.InputStream
import java.net.URLEncoder
import java.util.concurrent.TimeUnit

/** 待同步的一个文件（路径是「日期/文件名」这样的相对路径）。 */
data class RemoteItem(val path: String, val size: Long, val mtime: Long)

class Api(private val p: PrefsData) {

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(120, TimeUnit.SECONDS)
        .writeTimeout(120, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    private fun builder(path: String) = Request.Builder()
        .url("http://${p.host}:${p.port}$path")
        .header("X-Token", p.token)

    /** 探测电脑端是否在线。 */
    fun ping(): Boolean = try {
        client.newCall(builder("/api/ping").build()).execute().use { it.isSuccessful }
    } catch (e: Exception) {
        false
    }

    /**
     * 无感核对：把手机本地清单一次性报给电脑，电脑只回「哪些还需要传」。
     * 已经存在且大小、时间一致的文件会被跳过，不产生任何流量。
     */
    fun check(items: List<RemoteItem>): Set<String> {
        val arr = JSONArray()
        items.forEach {
            arr.put(
                JSONObject()
                    .put("path", it.path)
                    .put("size", it.size)
                    .put("mtime", it.mtime)
            )
        }
        val json = JSONObject().put("items", arr).toString()
        val body = json.toRequestBody("application/json; charset=utf-8".toMediaType())
        client.newCall(builder("/api/check").post(body).build()).execute().use { resp ->
            if (!resp.isSuccessful) throw RuntimeException("核对失败 HTTP ${resp.code}")
            val obj = JSONObject(resp.body?.string() ?: "{}")
            val need = obj.optJSONArray("need") ?: JSONArray()
            return (0 until need.length()).map { need.getString(it) }.toSet()
        }
    }

    /** 流式上传，边读边发，不占内存；回调已发送字节数用于显示进度。 */
    fun put(item: RemoteItem, stream: InputStream, onBytes: (Long) -> Unit): Boolean {
        val body = object : RequestBody() {
            override fun contentType() = "application/octet-stream".toMediaType()
            override fun contentLength() = item.size
            override fun writeTo(sink: BufferedSink) {
                val buf = ByteArray(128 * 1024)
                var total = 0L
                while (true) {
                    val n = stream.read(buf)
                    if (n < 0) break
                    sink.write(buf, 0, n)
                    total += n
                    onBytes(total)
                }
            }
        }
        val enc = URLEncoder.encode(item.path, "UTF-8")
        val req = builder("/api/put?path=$enc")
            .header("X-Mtime", item.mtime.toString())
            .put(body)
            .build()
        return try {
            client.newCall(req).execute().use { it.isSuccessful }
        } catch (e: Exception) {
            false
        } finally {
            runCatching { stream.close() }
        }
    }
}
