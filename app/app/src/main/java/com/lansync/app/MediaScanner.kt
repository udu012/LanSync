package com.lansync.app

import android.content.ContentUris
import android.content.Context
import android.net.Uri
import android.provider.MediaStore
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

data class MediaItem(val uri: Uri, val path: String, val size: Long, val mtime: Long)

/**
 * 扫描相册里的图片和视频，按「拍摄日期/文件名」生成目标路径。
 * 与电脑端的归档规则一致，保证无感核对能对上号。
 */
object MediaScanner {

    private val dayFmt = SimpleDateFormat("yyyy-MM-dd", Locale.US)

    fun scan(ctx: Context): List<MediaItem> {
        val out = ArrayList<MediaItem>(512)
        val cols = arrayOf(
            MediaStore.Files.FileColumns._ID,
            MediaStore.Files.FileColumns.DISPLAY_NAME,
            MediaStore.Files.FileColumns.SIZE,
            MediaStore.Files.FileColumns.DATE_MODIFIED,
            MediaStore.Files.FileColumns.MEDIA_TYPE
        )
        val sel = "${MediaStore.Files.FileColumns.MEDIA_TYPE}=? OR ${MediaStore.Files.FileColumns.MEDIA_TYPE}=?"
        val args = arrayOf(
            MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE.toString(),
            MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO.toString()
        )

        val uri = MediaStore.Files.getContentUri("external")
        val cursor = try {
            ctx.contentResolver.query(
                uri, cols, sel, args,
                "${MediaStore.Files.FileColumns.DATE_MODIFIED} DESC"
            )
        } catch (e: Exception) {
            null
        } ?: return out

        val seen = HashMap<String, Int>()

        cursor.use { c ->
            val iId = c.getColumnIndexOrThrow(MediaStore.Files.FileColumns._ID)
            val iName = c.getColumnIndexOrThrow(MediaStore.Files.FileColumns.DISPLAY_NAME)
            val iSize = c.getColumnIndexOrThrow(MediaStore.Files.FileColumns.SIZE)
            val iMod = c.getColumnIndexOrThrow(MediaStore.Files.FileColumns.DATE_MODIFIED)

            while (c.moveToNext()) {
                val id = c.getLong(iId)
                val rawName = c.getString(iName) ?: continue
                val size = c.getLong(iSize)
                val modSec = c.getLong(iMod)
                if (size <= 0) continue

                val name = sanitize(rawName)
                val day = dayFmt.format(Date(modSec * 1000))
                var path = "$day/$name"
                // 同一天同名的不同文件（比如两个 IMG_0001.jpg）自动加序号，避免互相覆盖
                val n = seen.getOrElse(path) { 0 }
                seen[path] = n + 1
                if (n > 0) {
                    val dot = name.lastIndexOf('.')
                    path = if (dot > 0) {
                        "$day/${name.substring(0, dot)} ($n)${name.substring(dot)}"
                    } else {
                        "$day/$name ($n)"
                    }
                }

                out.add(
                    MediaItem(
                        uri = ContentUris.withAppendedId(uri, id),
                        path = path,
                        size = size,
                        mtime = modSec
                    )
                )
            }
        }
        return out
    }

    private fun sanitize(s: String): String {
        var r = s.replace(Regex("[\\\\/:*?\"<>|]"), "_").trim()
        r = r.trimEnd('.', ' ')
        if (r.isEmpty()) r = "unnamed"
        return r
    }
}
