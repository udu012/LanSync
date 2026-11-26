package com.lansync.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.widget.Button
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.app.ActivityCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class MainActivity : AppCompatActivity() {

    private lateinit var tvStatus: TextView
    private lateinit var tvLast: TextView
    private lateinit var tvScope: TextView
    private lateinit var btnPair: Button
    private lateinit var btnSync: Button
    private lateinit var btnAuto: Button

    /** 备份范围摘要要扫一遍媒体库，10 秒内重复回到主页就不用再扫。 */
    private var scopeAt = 0L
    private var scopeSeq = 0

    private val timeFmt = SimpleDateFormat("MM-dd HH:mm", Locale.getDefault())

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val text = result.contents
        if (text.isNullOrBlank()) return@registerForActivityResult
        handlePair(text)
    }

    /** 从「备份范围」返回时立刻重算摘要，别让用户看到过期的数字。 */
    private val albumLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { refreshScope(force = true) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        Notifications.ensureChannels(this)

        tvStatus = findViewById(R.id.tv_status)
        tvLast = findViewById(R.id.tv_last)
        tvScope = findViewById(R.id.tv_scope)
        btnPair = findViewById(R.id.btn_pair)
        btnSync = findViewById(R.id.btn_sync)
        btnAuto = findViewById(R.id.btn_auto)

        btnPair.setOnClickListener { startScan() }

        findViewById<android.view.View>(R.id.card_scope).setOnClickListener {
            albumLauncher.launch(Intent(this, AlbumActivity::class.java))
        }
        findViewById<android.view.View>(R.id.card_guide).setOnClickListener {
            startActivity(Intent(this, GuideActivity::class.java))
        }

        btnSync.setOnClickListener {
            if (!Prefs.load(this).paired) {
                toast("请先扫描电脑上的二维码")
                return@setOnClickListener
            }
            SyncService.start(this)
            toast("开始同步…")
        }

        btnAuto.setOnClickListener {
            val p = Prefs.load(this)
            val next = p.copy(auto = !p.auto)
            Prefs.save(this, next)
            if (next.auto) {
                Scheduler.enable(this)
                WatchService.start(this) // 常驻守护：拍完照几秒就自动备份
                askBattery()
            } else {
                Scheduler.disable(this)
                WatchService.stop(this)
            }
            refresh()
        }

        ensurePermissions()
    }

    override fun onResume() {
        super.onResume()
        refresh()
        // 配对过又开着自动备份，就把守护服务拉起来（进程被系统清过也能自愈）
        val p = Prefs.load(this)
        if (p.paired && p.auto) WatchService.start(this)
        refreshScope()
    }

    private fun refresh() {
        val p = Prefs.load(this)
        if (!p.paired) {
            tvStatus.text = "未配对"
            tvLast.text = "点下面的按钮，扫描电脑屏幕上显示的二维码即可开始"
        } else {
            tvStatus.text = "已连接 ${p.host}:${p.port}"
            val last = if (p.lastSyncAt > 0)
                timeFmt.format(Date(p.lastSyncAt)) else "还没同步过"
            tvLast.text = "上次同步：$last\n${p.lastResult.ifEmpty { "点「立即同步」试一次" }}"
        }
        btnAuto.text = if (p.auto) getString(R.string.auto_on) else getString(R.string.auto_off)
    }

    /**
     * 刷新「备份范围」那一行：全部相册还是关掉了几个、还剩多少体积。
     * 全量统计要走一遍媒体库，放后台线程，几秒内重复回主页不再重复扫。
     */
    private fun refreshScope(force: Boolean = false) {
        if (!force && System.currentTimeMillis() - scopeAt < 10_000) return
        scopeAt = System.currentTimeMillis()
        val seq = ++scopeSeq
        Thread {
            val albums = try {
                Albums.scan(this)
            } catch (e: Exception) {
                emptyList<Album>()
            }
            val ex = Prefs.load(this).excluded
            runOnUiThread {
                if (seq != scopeSeq || isFinishing) return@runOnUiThread
                renderScope(albums, ex)
            }
        }.start()
    }

    private fun renderScope(albums: List<Album>, ex: Set<String>) {
        if (albums.isEmpty()) {
            tvScope.text = "还没读到照片"
            tvScope.setTextColor(ContextCompat.getColor(this, R.color.textMuted))
            return
        }
        val off = albums.count { Albums.isExcluded(it.dir, ex) }
        val on = albums.size - off

        if (on == 0) {
            tvScope.text = "全关掉了 · 不会上传任何照片"
            tvScope.setTextColor(0xFFB45309.toInt())
            return
        }
        tvScope.setTextColor(ContextCompat.getColor(this, R.color.textMuted))
        if (off == 0) {
            val bytes = albums.sumOf { it.bytes }
            tvScope.text = "全部 ${albums.size} 个相册 · ${Albums.sizeText(bytes)}"
        } else {
            val bytes = albums.filter { !Albums.isExcluded(it.dir, ex) }.sumOf { it.bytes }
            tvScope.text = "已关 $off 个 · 备份 $on 个 · ${Albums.sizeText(bytes)}"
        }
    }

    private fun startScan() {
        val opts = ScanOptions().apply {
            setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            setPrompt("对准电脑屏幕上的二维码")
            setBeepEnabled(false)
            // 扫码页默认会跟着重力传感器转：手一歪就变横屏，二维码框爬起来很别扭。
            // 锁成竖屏，跟这个页面（也跟着手机）一致。
            setOrientationLocked(true)
        }
        scanLauncher.launch(opts)
    }

    /** 二维码内容形如 http://192.168.1.5:8765/?t=xxxx */
    private fun handlePair(text: String) {
        val uri = try {
            Uri.parse(text.trim())
        } catch (e: Exception) {
            null
        }
        val host = uri?.host
        val token = uri?.getQueryParameter("t")
        if (uri == null || host.isNullOrBlank() || token.isNullOrBlank()) {
            toast("这个二维码不是 LanSync 的配对码")
            return
        }
        val port = if (uri.port > 0) uri.port else 8765
        val p = Prefs.load(this).copy(host = host, port = port, token = token)
        Prefs.save(this, p)

        tvStatus.text = "已连接 $host:$port"
        Toast.makeText(this, "配对成功，正在试连…", Toast.LENGTH_SHORT).show()

        Thread {
            val ok = Api(p).ping()
            runOnUiThread {
                if (ok) {
                    toast("连接正常，可以开始备份了")
                    if (p.auto) {
                        Scheduler.enable(this)
                        WatchService.start(this)
                        askBattery()
                    }
                    SyncService.start(this)
                } else {
                    toast("配对已保存，但暂时连不上电脑（确认同一 Wi-Fi）")
                }
                refresh()
            }
        }.start()
    }

    private fun ensurePermissions() {
        val want = mutableListOf<String>()
        if (Build.VERSION.SDK_INT >= 33) {
            if (!granted(Manifest.permission.READ_MEDIA_IMAGES)) want += Manifest.permission.READ_MEDIA_IMAGES
            if (!granted(Manifest.permission.READ_MEDIA_VIDEO)) want += Manifest.permission.READ_MEDIA_VIDEO
            if (!granted(Manifest.permission.POST_NOTIFICATIONS)) want += Manifest.permission.POST_NOTIFICATIONS
        } else {
            if (!granted(Manifest.permission.READ_EXTERNAL_STORAGE)) want += Manifest.permission.READ_EXTERNAL_STORAGE
        }
        if (want.isNotEmpty()) {
            ActivityCompat.requestPermissions(this, want.toTypedArray(), 100)
        }
    }

    private fun granted(p: String) =
        ContextCompat.checkSelfPermission(this, p) == PackageManager.PERMISSION_GRANTED

    /** 引导用户关掉电池优化，否则国产 ROM 会在后台把同步杀掉。 */
    private fun askBattery() {
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        if (pm.isIgnoringBatteryOptimizations(packageName)) return
        try {
            startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
            toast("请在列表里找到 LanSync，设为「不限制」")
        } catch (e: Exception) {
            // 忽略
        }
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_SHORT).show()
}
