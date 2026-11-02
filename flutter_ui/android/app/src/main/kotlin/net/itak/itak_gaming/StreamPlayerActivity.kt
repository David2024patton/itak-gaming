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
        val embed = if (!videoId.isNullOrEmpty()) {
            "https://www.youtube.com/embed/$videoId"
        } else {
            "https://www.youtube.com/embed/live_stream?channel=$channelId"
        }
        val wv = WebView(this)
        webView = wv
        val settings = wv.settings
        settings.javaScriptEnabled = true
        settings.domStorageEnabled = true
        settings.mediaPlaybackRequiresUserGesture = false
        settings.loadWithOverviewMode = true
        settings.useWideViewPort = true
        // YouTube refuses embedded playback to the stock WebView
        // user agent and to pages with no real referrer: the
        // first build's iframe wrapper (loadDataWithBaseURL) got
        // error 152 on every live channel on the Missouri stick
        // (proven 2026-11-02). What works is loading the embed
        // URL as the page itself, with a desktop Chrome user
        // agent, so the player sees an ordinary browser tab.
        settings.userAgentString =
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
            "AppleWebKit/537.36 (KHTML, like Gecko) " +
            "Chrome/130.0.0.0 Safari/537.36"
        wv.webViewClient = WebViewClient()
        wv.webChromeClient = WebChromeClient()
        wv.loadUrl("$embed?autoplay=1&playsinline=1&rel=0")
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
