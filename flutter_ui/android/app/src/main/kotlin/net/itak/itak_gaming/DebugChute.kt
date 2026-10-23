package net.itak.itak_gaming

import android.content.Context
import android.os.Build
import android.os.Environment
import android.os.StatFs
import android.util.Log
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.RandomAccessFile
import java.net.HttpURLConnection
import java.net.URL
import java.security.SecureRandom
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone
import java.util.concurrent.TimeUnit

// ---------------------------------------------------------------------------
// Debug chute, native half: a WorkManager check-in that pings our
// own server about every 15 minutes (WorkManager's minimum period;
// the stick is mains powered and usually awake). The app only ever
// sends. The ping is tiny: identity, version, model, free storage,
// and the newest few core timing lines. The server answers
// {"want_full_report": true|false}; when true, this same check-in
// immediately uploads the full report (log tail included, reason
// "pull"), which is how a full log is pulled on demand with no
// inbound connection to the stick at all.
//
// Identity and the on/off switch live in the same itak_prefs
// SharedPreferences the Dart side uses: 'install_code' (16 random
// bytes as hex, generated once by whichever side runs first) and
// 'debug_chute' ('0' means off, anything else including unset
// means on). With the switch off the worker is a no-op.
// ---------------------------------------------------------------------------

object DebugChute {
    private const val TAG = "DebugChute"
    private const val SERVER = "https://arcade.itak.live"
    private const val PREFS = "itak_prefs"

    fun enabled(ctx: Context): Boolean {
        val prefs = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        return prefs.getString("debug_chute", null) != "0"
    }

    fun installCode(ctx: Context): String {
        val prefs = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val existing = prefs.getString("install_code", null)
        if (existing != null && existing.length == 32 &&
            existing.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }
        ) {
            return existing
        }
        val bytes = ByteArray(16)
        SecureRandom().nextBytes(bytes)
        val code = bytes.joinToString("") { "%02x".format(it) }
        prefs.edit().putString("install_code", code).apply()
        return code
    }

    private fun logFile(ctx: Context): File = File(File(ctx.filesDir, "logs"), "itak.log")

    // Last [maxBytes] of the app log file as text ("" when none).
    private fun logTailText(ctx: Context, maxBytes: Long): String {
        val file = logFile(ctx)
        if (!file.exists()) return ""
        val size = file.length()
        val want = minOf(size, maxBytes)
        if (want <= 0) return ""
        val buf = ByteArray(want.toInt())
        RandomAccessFile(file, "r").use { raf ->
            raf.seek(size - want)
            raf.readFully(buf)
        }
        var text = String(buf, Charsets.UTF_8)
        if (want < size) {
            val nl = text.indexOf('\n')
            if (nl >= 0) text = text.substring(nl + 1)
        }
        return text
    }

    // The last [max] "native timing" lines in the app log.
    fun timingLines(ctx: Context, max: Int): List<String> {
        val text = logTailText(ctx, 512 * 1024L)
        if (text.isEmpty()) return emptyList()
        val hits = text.split('\n').filter { it.contains("native timing") }
        return if (hits.size <= max) hits else hits.subList(hits.size - max, hits.size)
    }

    private fun freeStorageMB(): Long {
        return try {
            val fs = StatFs(Environment.getDataDirectory().path)
            fs.availableBytes / (1024 * 1024)
        } catch (e: Exception) {
            -1
        }
    }

    private fun utcNow(): String {
        val fmt = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US)
        fmt.timeZone = TimeZone.getTimeZone("UTC")
        return fmt.format(Date())
    }

    private fun versionInfo(ctx: Context): Pair<String, Int> {
        return try {
            val info = ctx.packageManager.getPackageInfo(ctx.packageName, 0)
            @Suppress("DEPRECATION")
            val code =
                if (Build.VERSION.SDK_INT >= 28) info.longVersionCode.toInt() else info.versionCode
            (info.versionName ?: "") to code
        } catch (e: Exception) {
            "" to 0
        }
    }

    private fun baseJson(ctx: Context): JSONObject {
        val (vName, vCode) = versionInfo(ctx)
        val o = JSONObject()
        o.put("install_code", installCode(ctx))
        o.put("versionName", vName)
        o.put("versionCode", vCode)
        o.put("model", Build.MODEL ?: "unknown")
        o.put("sdk", Build.VERSION.SDK_INT)
        val free = freeStorageMB()
        if (free >= 0) o.put("freeStorageMB", free)
        o.put("timestamp", utcNow())
        return o
    }

    // POST JSON; returns (status, body) or null on a network error.
    private fun post(path: String, body: JSONObject): Pair<Int, String>? {
        var conn: HttpURLConnection? = null
        return try {
            conn = (URL(SERVER + path).openConnection() as HttpURLConnection).apply {
                requestMethod = "POST"
                connectTimeout = 15_000
                readTimeout = 15_000
                doOutput = true
                setRequestProperty("Content-Type", "application/json")
                setRequestProperty("User-Agent", "iTaK-Gaming/1.0")
            }
            conn.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
            val code = conn.responseCode
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            val text = stream?.bufferedReader()?.use { it.readText() } ?: ""
            code to text
        } catch (e: Exception) {
            Log.w(TAG, "POST $path failed", e)
            null
        } finally {
            conn?.disconnect()
        }
    }

    // One full report upload. True on a 200.
    fun sendFull(ctx: Context, reason: String): Boolean {
        val o = baseJson(ctx)
        o.put("timingLines", JSONArray(timingLines(ctx, 40)))
        o.put("logTail", logTailText(ctx, 120 * 1024L))
        o.put("reason", reason)
        val res = post("/debug/report", o) ?: return false
        Log.i(TAG, "full report ($reason) -> ${res.first}")
        return res.first == 200
    }

    // One check-in: ping, then the full report too when the server
    // asks for it. True when everything the server wanted is done.
    fun checkIn(ctx: Context): Boolean {
        val o = baseJson(ctx)
        o.put("timingLines", JSONArray(timingLines(ctx, 10)))
        val res = post("/debug/ping", o) ?: return false
        if (res.first != 200) {
            Log.w(TAG, "ping refused: ${res.first}")
            return false
        }
        val want = try {
            JSONObject(res.second).optBoolean("want_full_report", false)
        } catch (e: Exception) {
            false
        }
        Log.i(TAG, "ping ok, want_full_report=$want")
        return if (want) sendFull(ctx, "pull") else true
    }

    // Schedule the 15-minute periodic check-in (kept across app
    // updates) and one immediate check-in per app start, so a
    // fresh install checks in right away and a pending pull is
    // picked up at the next launch at the latest.
    fun enqueue(ctx: Context) {
        try {
            val appCtx = ctx.applicationContext
            val wm = WorkManager.getInstance(appCtx)
            val constraints = Constraints.Builder()
                .setRequiredNetworkType(NetworkType.CONNECTED)
                .build()
            val periodic = PeriodicWorkRequestBuilder<DebugChuteWorker>(15, TimeUnit.MINUTES)
                .setConstraints(constraints)
                .build()
            wm.enqueueUniquePeriodicWork(
                "debug_chute_periodic",
                ExistingPeriodicWorkPolicy.KEEP,
                periodic,
            )
            val now = OneTimeWorkRequestBuilder<DebugChuteWorker>()
                .setConstraints(constraints)
                .build()
            wm.enqueueUniqueWork("debug_chute_now", ExistingWorkPolicy.REPLACE, now)
        } catch (e: Exception) {
            Log.w(TAG, "enqueue failed", e)
        }
    }
}

class DebugChuteWorker(ctx: Context, params: WorkerParameters) : Worker(ctx, params) {
    override fun doWork(): Result {
        val ctx = applicationContext
        if (!DebugChute.enabled(ctx)) return Result.success()
        return if (DebugChute.checkIn(ctx)) Result.success() else Result.retry()
    }
}
