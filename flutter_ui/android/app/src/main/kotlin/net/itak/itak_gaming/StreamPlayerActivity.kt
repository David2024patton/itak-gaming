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
        wv.webViewClient = WebViewClient()
        wv.webChromeClient = WebChromeClient()
        // The iframe wrapper with a youtube.com base URL is the
        // standard way to embed: loading the bare embed URL in a
        // WebView gets refused for many streams.
        val html = "<!DOCTYPE html><html><head>" +
            "<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
            "<style>html,body{margin:0;padding:0;background:#000;height:100%}" +
            "iframe{width:100%;height:100%;border:0}</style></head>" +
            "<body><iframe src=\"$embed?autoplay=1&rel=0\" " +
            "allow=\"autoplay; encrypted-media; fullscreen\" allowfullscreen>" +
            "</iframe></body></html>"
        wv.loadDataWithBaseURL(
            "https://www.youtube.com", html, "text/html", "utf-8", null
        )
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
