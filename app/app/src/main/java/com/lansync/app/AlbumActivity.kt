package com.lansync.app

import android.os.Bundle
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.google.android.material.button.MaterialButton
import com.google.android.material.switchmaterial.SwitchMaterial

/**
 * 「备份范围」：把手机上的相册列出来，让用户勾掉不想备份的（比如微信存下来的图）。
 *
 * 存的是排除名单，所以老用户升级上来默认全开，新装的 App 新增的相册也自动纳入。
 * 每次改动立刻落盘，用户点左上角返回还是「完成」都不会丢。
 */
class AlbumActivity : AppCompatActivity() {

    private lateinit var list: LinearLayout
    private lateinit var tvCount: TextView
    private lateinit var tvLoading: TextView

    private var albums: List<Album> = emptyList()
    private val excluded = HashSet<String>()
    private val rows = HashMap<String, SwitchMaterial>()

    /** 批量改开关时先屏蔽监听，免得每改一行就写一次盘。 */
    private var binding = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_albums)

        list = findViewById(R.id.list)
        tvCount = findViewById(R.id.tv_count)
        tvLoading = findViewById(R.id.tv_loading)

        excluded.addAll(Prefs.load(this).excluded)

        findViewById<View>(R.id.btn_back).setOnClickListener { finish() }
        findViewById<MaterialButton>(R.id.btn_done).setOnClickListener { finish() }
        findViewById<View>(R.id.btn_all).setOnClickListener { setAll(true) }
        findViewById<View>(R.id.btn_none).setOnClickListener { setAll(false) }

        load()
    }

    /** 统计要遍历整个媒体库，几千张照片约 1 秒，不能放主线程。 */
    private fun load() {
        tvLoading.visibility = View.VISIBLE
        Thread {
            val r = try {
                Albums.scan(this)
            } catch (e: Exception) {
                emptyList<Album>()
            }
            runOnUiThread {
                if (isFinishing) return@runOnUiThread
                albums = r
                build()
                tvLoading.visibility = View.GONE
            }
        }.start()
    }

    private fun build() {
        list.removeAllViews()
        rows.clear()

        if (albums.isEmpty()) {
            val tv = TextView(this)
            tv.text = "还没读到照片。\n请先在系统设置里允许 LanSync 访问「照片和视频」，再回来看看。"
            tv.setTextColor(ContextCompat.getColor(this, R.color.textMuted))
            tv.textSize = 13f
            tv.setLineSpacing(0f, 1.5f)
            val p = (24 * resources.displayMetrics.density).toInt()
            tv.setPadding(p, p, p, p)
            list.addView(tv)
            updateCount()
            return
        }

        for (a in albums) {
            val row = layoutInflater.inflate(R.layout.item_album, list, false)
            row.findViewById<TextView>(R.id.tv_title).text = a.title
            row.findViewById<TextView>(R.id.tv_sub).text = a.subtitle

            val sw = row.findViewById<SwitchMaterial>(R.id.sw)
            sw.isChecked = !Albums.isExcluded(a.dir, excluded)
            sw.setOnCheckedChangeListener { _, checked ->
                if (!binding) toggle(a.dir, checked)
            }
            // 整行都能点，不用非得戳那个小开关
            row.setOnClickListener { sw.isChecked = !sw.isChecked }

            rows[a.dir] = sw
            list.addView(row)
        }
        updateCount()
    }

    private fun toggle(dir: String, on: Boolean) {
        if (on) excluded.remove(dir) else excluded.add(dir)
        persist()
        updateCount()
        if (!on && allOff()) toast("全都关掉了，将不会上传任何照片")
    }

    private fun setAll(on: Boolean) {
        binding = true
        if (on) {
            excluded.clear()
        } else {
            excluded.addAll(albums.map { it.dir })
        }
        for ((dir, sw) in rows) {
            sw.isChecked = !Albums.isExcluded(dir, excluded)
        }
        binding = false

        persist()
        updateCount()
        if (!on && albums.isNotEmpty()) toast("全都关掉了，将不会上传任何照片")
    }

    private fun allOff() = albums.isNotEmpty() && albums.all { Albums.isExcluded(it.dir, excluded) }

    private fun persist() {
        // 还没统计出相册时别写盘，否则「全选」会把用户原来的名单清空
        if (albums.isEmpty()) return
        Prefs.save(this, Prefs.load(this).copy(excluded = excluded.toSet()))
    }

    private fun updateCount() {
        if (albums.isEmpty()) {
            tvCount.text = "没有相册"
            return
        }
        val off = albums.count { Albums.isExcluded(it.dir, excluded) }
        val on = albums.size - off
        if (on == 0) {
            tvCount.text = "一个都没选"
            tvCount.setTextColor(0xFFB45309.toInt())
        } else {
            tvCount.text = "已选 $on / ${albums.size}"
            tvCount.setTextColor(ContextCompat.getColor(this, R.color.textMuted))
        }
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_SHORT).show()
}
