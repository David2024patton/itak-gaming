package net.itak.itak_gaming

import android.app.Activity
import android.os.Bundle
import android.view.KeyEvent
import android.view.WindowManager
import android.webkit.WebChromeClient
import android.webkit.WebView
import android.webkit.WebViewClient

// Full-screen player for the PATRIOTS livestreams (David,
// 2026-11-02). Terry's stick has no YouTube app (removed on
// David's order), so streams play here, in YouTube's own
// embedded player inside a WebView. When the PATRIOTS screen
// pulled the channel's current live video id, that broadcast is
// embedded directly; otherwise the channel's permanent live
// embed is used, which always resolves to whatever is live now.
class StreamPlayerActivity : Activity() {

    private var webView: WebView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        val videoId = intent.getStringExtra("videoId")
        val channelId = intent.getStringExtra("channelId")
        // YouTube's embed player only plays when the page framing
        // it is served over HTTPS from a real domain, so the
        // iframe request carries a genuine Referer. Proven on the
        // Missouri stick (2026-11-02): an in-app iframe wrapper
        // (loadDataWithBaseURL) got error 152 on every channel,
        // and loading the bare embed URL with a spoofed desktop
        // user agent got error 153. The player therefore loads
        // our own tiny host page on the update service, which
        // frames the channel's embed; the WebView itself stays
        // completely stock.
        val page = if (!videoId.isNullOrEmpty()) {
            "https://arcade.itak.live/app/embed.html?v=$videoId"
        } else {
            "https://arcade.itak.live/app/embed.html?c=$channelId"
        }
        val wv = WebView(this)
        webView = wv
        val settings = wv.settings
        settings.javaScriptEnabled = true
        settings.domStorageEnabled = true
        settings.mediaPlaybackRequiresUserGesture = false
        settings.loadWithOverviewMode = true
        settings.useWideViewPort = true
        wv.webViewClient = WebViewClient()
        wv.webChromeClient = WebChromeClient()
        wv.loadUrl(page)
        setContentView(wv)
    }

    override fun onPause() {
        webView?.onPause()
        super.onPause()
    }

    override fun onResume() {
        super.onResume()
        webView?.onResume()
    }

    override fun onDestroy() {
        webView?.let {
            it.stopLoading()
            it.destroy()
        }
        webView = null
        super.onDestroy()
    }

    // BACK leaves the player. Every other key keeps its default
    // behavior so the volume and media keys reach the system.
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (event.keyCode == KeyEvent.KEYCODE_BACK &&
            event.action == KeyEvent.ACTION_DOWN
        ) {
            finish()
            return true
        }
        return super.dispatchKeyEvent(event)
    }
}
