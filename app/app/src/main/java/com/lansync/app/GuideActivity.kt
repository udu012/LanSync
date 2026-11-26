package com.lansync.app

import android.annotation.SuppressLint
import android.os.Bundle
import android.view.View
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity

/**
 * 图文使用说明。
 *
 * 说明本身是一张 2160×15868 的长图，整张放进 APK 要 3.7MB，解码还要 130MB 内存，
 * 低端机直接 OOM。所以这里换成 HTML + 6 张 WebP：合计 300KB 左右，
 * 文字还能选中、能跟着屏幕宽度重排，比缩到 0.36 倍的长图清楚得多。
 */
class GuideActivity : AppCompatActivity() {

    private lateinit var web: WebView
    private lateinit var fallback: View

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_guide)

        findViewById<View>(R.id.btn_back).setOnClickListener { finish() }
        web = findViewById(R.id.web)
        fallback = findViewById(R.id.fallback)

        web.setBackgroundColor(0xFFEEF1F6.toInt())
        web.settings.apply {
            javaScriptEnabled = true // 只用于「点击图片放大」，不加载任何外部内容
            // 双指放大：截图里的小字靠它看清
            setSupportZoom(true)
            builtInZoomControls = true
            displayZoomControls = false
            useWideViewPort = true
            loadWithOverviewMode = false
            allowFileAccess = true
            // 本地资源，顺手把联网能力也关掉
            blockNetworkLoads = true
        }

        web.webViewClient = object : WebViewClient() {
            override fun onPageFinished(view: WebView, url: String?) {
                super.onPageFinished(view, url)
                checkRendered(view)
            }

            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: android.webkit.WebResourceError
            ) {
                if (request.isForMainFrame) showFallback()
            }
        }

        web.loadUrl("file:///android_asset/guide/guide.html")
    }

    /**
     * 白屏兜底：个别 ROM 的 WebView 会「加载成功但什么都没画出来」，
     * 光靠 onReceivedError 抓不到，只能等它渲染完再问一句正文有没有内容。
     */
    private fun checkRendered(view: WebView) {
        view.postDelayed({
            if (isFinishing || isDestroyed) return@postDelayed
            view.evaluateJavascript(
                "(document.body && document.body.innerText || '').length"
            ) { raw ->
                val n = raw?.trim()?.trim('"')?.toIntOrNull() ?: 0
                if (n < 200) showFallback()
            }
        }, 1500)
    }

    private fun showFallback() {
        if (isFinishing || isDestroyed) return
        fallback.visibility = View.VISIBLE
        web.visibility = View.GONE
    }

    override fun onDestroy() {
        try {
            web.stopLoading()
            web.destroy()
        } catch (e: Exception) {
            // 个别 ROM 在页面还在跑 JS 时 destroy 会抛，忽略
        }
        super.onDestroy()
    }
}
