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
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class MainActivity : AppCompatActivity() {

    private lateinit var tvStatus: TextView
    private lateinit var tvLast: TextView
    private lateinit var btnPair: Button
    private lateinit var btnSync: Button
    private lateinit var btnAuto: Button

    private val timeFmt = SimpleDateFormat("MM-dd HH:mm", Locale.getDefault())

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val text = result.contents
        if (text.isNullOrBlank()) return@registerForActivityResult
        handlePair(text)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        Notifications.ensureChannels(this)

        tvStatus = findViewById(R.id.tv_status)
        tvLast = findViewById(R.id.tv_last)
        btnPair = findViewById(R.id.btn_pair)
        btnSync = findViewById(R.id.btn_sync)
        btnAuto = findViewById(R.id.btn_auto)

        btnPair.setOnClickListener { startScan() }

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
                askBattery()
            } else {
                Scheduler.disable(this)
            }
            refresh()
        }

        ensurePermissions()
    }

    override fun onResume() {
        super.onResume()
        refresh()
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

    private fun startScan() {
        val opts = ScanOptions().apply {
            setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            setPrompt("对准电脑屏幕上的二维码")
            setBeepEnabled(false)
            setOrientationLocked(false)
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
