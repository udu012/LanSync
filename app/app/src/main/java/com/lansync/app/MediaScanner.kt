package com.lansync.app

import android.content.ContentUris
import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.MediaStore

data class MediaItem(val uri: Uri, val path: String, val size: Long, val mtime: Long)

/**
 * 扫描相册里的图片和视频，按手机上的原始目录结构生成目标路径。
 *
 * v1.2 是「日期/文件名」归档，照片一多就变成一堆按天分的文件夹，找东西全靠回忆日期。
 * v1.3 改成照搬手机的目录：DCIM/Camera/IMG_0001.jpg、Pictures/WeChat/xxx.jpg，
 * 电脑上打开就是手机里那套熟悉的结构。
 */
object MediaScanner {

    /** 拿不到 RELATIVE_PATH 的旧机型 / 个别目录，兜底放这里。 */
    private const val FALLBACK_DIR = "未分类"

    fun scan(ctx: Context): List<MediaItem> {
        val out = ArrayList<MediaItem>(512)

        // RELATIVE_PATH 是 Android 10 才有的列，老系统要退回 DATA（绝对路径）。
        val useRelative = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q
        val dirCol = if (useRelative) {
            MediaStore.MediaColumns.RELATIVE_PATH
        } else {
            @Suppress("DEPRECATION")
            MediaStore.MediaColumns.DATA
        }

        val cols = arrayOf(
            MediaStore.Files.FileColumns._ID,
            MediaStore.Files.FileColumns.DISPLAY_NAME,
            MediaStore.Files.FileColumns.SIZE,
            MediaStore.Files.FileColumns.DATE_MODIFIED,
            dirCol
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
            val iDir = c.getColumnIndex(dirCol)

            while (c.moveToNext()) {
                val id = c.getLong(iId)
                val rawName = c.getString(iName) ?: continue
                val size = c.getLong(iSize)
                val modSec = c.getLong(iMod)
                if (size <= 0) continue

                val name = sanitize(rawName)
                val rawDir = if (iDir >= 0) {
                    val v = c.getString(iDir)
                    if (useRelative) relativeDir(v) else absoluteToRelative(v)
                } else {
                    FALLBACK_DIR
                }
                // 目录名可能是电脑上不合法的字符（个别 App 会建 "2024:01" 这种目录），
                // 逐段清洗，避免 Windows 上建不出目录导致整个文件传失败。
                val dir = rawDir.split('/')
                    .filter { it.isNotEmpty() }
                    .joinToString("/") { sanitize(it) }

                var path = if (dir.isEmpty()) "$FALLBACK_DIR/$name" else "$dir/$name"

                // 同一目录下同名的不同文件（比如两个 IMG_0001.jpg）自动加序号，
                // 避免后传的把先传的覆盖掉。
                val n = seen.getOrElse(path) { 0 }
                seen[path] = n + 1
                if (n > 0) {
                    val dot = name.lastIndexOf('.')
                    path = if (dot > 0) {
                        "$dir/${name.substring(0, dot)} ($n)${name.substring(dot)}"
                    } else {
                        "$dir/$name ($n)"
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

    /** "DCIM/Camera/" → "DCIM/Camera"；去掉首尾斜杠，空值返回空串。 */
    private fun relativeDir(v: String?): String =
        (v ?: "").replace('\\', '/').trim('/')

    /** Android 9 及以下：DATA 是 "/storage/emulated/0/DCIM/Camera/a.jpg"，截掉存储根。 */
    @Suppress("DEPRECATION")
    private fun absoluteToRelative(v: String?): String {
        val p = (v ?: "").replace('\\', '/').trim()
        if (p.isEmpty()) return ""
        val prefixes = listOf(
            "/storage/emulated/0/",
            "/storage/emulated/",
            "/sdcard/",
            "/mnt/sdcard/"
        )
        var rest = p
        for (pre in prefixes) {
            if (rest.startsWith(pre)) {
                rest = rest.substring(pre.length)
                break
            }
        }
        val slash = rest.lastIndexOf('/')
        return if (slash > 0) rest.substring(0, slash).trim('/') else ""
    }

    private fun sanitize(s: String): String {
        var r = s.replace(Regex("[\\\\/:*?\"<>|]"), "_").trim()
        r = r.trimEnd('.', ' ')
        if (r.isEmpty()) r = "unnamed"
        return r
    }
}
