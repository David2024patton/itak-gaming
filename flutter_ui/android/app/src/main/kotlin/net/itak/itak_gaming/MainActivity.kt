package net.itak.itak_gaming

import android.content.Context
import android.content.Intent
import android.hardware.input.InputManager
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.InputDevice
import android.view.KeyEvent
import androidx.core.content.FileProvider
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel
import java.io.BufferedWriter
import java.io.File
import java.io.FileOutputStream
import java.io.FileWriter
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import kotlin.concurrent.thread

class MainActivity : FlutterActivity() {

    companion object {
        private val logLock = Object()
        private var logStarted = false
    }

    private var channel: MethodChannel? = null
    private var lastControllerPromptMs = 0L

    // Self-update download progress, streamed to Dart over the
    // update_progress event channel while downloadAndInstall runs
    // on its worker thread.
    @Volatile
    private var updateProgressSink: EventChannel.EventSink? = null
    private val mainHandler = Handler(Looper.getMainLooper())

    private fun reportUpdateProgress(downloaded: Long, total: Long) {
        if (updateProgressSink == null) return
        mainHandler.post {
            updateProgressSink?.success(
                mapOf("downloaded" to downloaded, "total" to total)
            )
        }
    }

    // Controller assignment capture: while armed, the next gamepad
    // button press identifies that controller to Dart.
    @Volatile
    private var captureArmed = false

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (captureArmed && event.action == KeyEvent.ACTION_DOWN) {
            val dev = event.device
            val gamepad = dev != null &&
                (dev.sources and InputDevice.SOURCE_GAMEPAD != 0 ||
                    dev.sources and InputDevice.SOURCE_JOYSTICK != 0)
            if (gamepad && event.keyCode != KeyEvent.KEYCODE_BACK) {
                captureArmed = false
                channel?.invokeMethod(
                    "controllerCaptured",
                    mapOf("id" to event.deviceId, "name" to (dev?.name ?: "Controller")),
                )
                return true
            }
        }
        return super.dispatchKeyEvent(event)
    }

    // When a gamepad shows up, ask the UI who picked it up. The TV
    // remote is not a gamepad source, so it never triggers this.
    private val deviceListener = object : InputManager.InputDeviceListener {
        override fun onInputDeviceAdded(deviceId: Int) {
            val dev = InputDevice.getDevice(deviceId) ?: return
            val gamepad = dev.sources and InputDevice.SOURCE_GAMEPAD != 0 ||
                dev.sources and InputDevice.SOURCE_JOYSTICK != 0
            if (!gamepad) return
            val now = System.currentTimeMillis()
            if (now - lastControllerPromptMs < 4000) return
            lastControllerPromptMs = now
            channel?.invokeMethod("controllerConnected", mapOf("name" to dev.name))
        }

        override fun onInputDeviceRemoved(deviceId: Int) {}
        override fun onInputDeviceChanged(deviceId: Int) {}
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val im = getSystemService(Context.INPUT_SERVICE) as InputManager
        im.registerInputDeviceListener(deviceListener, Handler(Looper.getMainLooper()))
        startLogCapture()
    }

    // ----- Debug log capture (Settings -> Logs) -----
    // Streams this app's own logcat (every Log.* line from the UI,
    // the emulator activity, the Go engine, and crashes) into a
    // rotating file the Logs screen can read, export, and clear.

    private fun logFile(): File {
        val dir = File(filesDir, "logs")
        if (!dir.exists()) dir.mkdirs()
        return File(dir, "itak.log")
    }

    private fun startLogCapture() {
        synchronized(logLock) {
            if (logStarted) return
            logStarted = true
        }
        thread(name = "log-capture") {
            try {
                val file = logFile()
                val pidFile = File(file.parentFile, "capture.pid")
                // Kill a logcat left behind by a previous run so
                // lines are never written twice.
                try {
                    val oldPid = pidFile.readText().trim().toInt()
                    if (oldPid > 0) android.os.Process.killProcess(oldPid)
                } catch (e: Exception) { /* none recorded */ }
                val ver = try {
                    packageManager.getPackageInfo(packageName, 0).versionName
                } catch (e: Exception) { "?" }
                var writer = BufferedWriter(FileWriter(file, true))
                writer.write("==== iTaK Gaming v$ver start ${java.util.Date()} ====\n")
                writer.flush()
                val cmd = "echo \$\$ > ${pidFile.absolutePath}; " +
                    "exec logcat -v threadtime --pid=${android.os.Process.myPid()}"
                val proc = Runtime.getRuntime().exec(arrayOf("sh", "-c", cmd))
                val reader = proc.inputStream.bufferedReader()
                var sinceFlush = 0
                var sinceCheck = 0
                while (true) {
                    val line = reader.readLine() ?: break
                    writer.write(line)
                    writer.write("\n")
                    sinceFlush++
                    sinceCheck++
                    if (sinceFlush >= 25) {
                        writer.flush()
                        sinceFlush = 0
                    }
                    if (sinceCheck >= 400) {
                        sinceCheck = 0
                        if (file.length() > 2_000_000L) {
                            writer.flush()
                            writer.close()
                            val old = File(file.parentFile, "itak.old.log")
                            if (old.exists()) old.delete()
                            file.renameTo(old)
                            writer = BufferedWriter(FileWriter(file, true))
                            writer.write("==== log rotated ${java.util.Date()} ====\n")
                            writer.flush()
                        }
                    }
                }
                writer.flush()
                writer.close()
            } catch (e: Exception) {
                android.util.Log.w("iTaKLog", "capture failed", e)
            }
        }
    }

    private fun readLogTail(): Map<String, Any> {
        val file = logFile()
        if (!file.exists()) return mapOf("text" to "", "size" to 0L)
        val size = file.length()
        val want = minOf(size, 200_000L)
        val buf = ByteArray(want.toInt())
        java.io.RandomAccessFile(file, "r").use { raf ->
            raf.seek(size - want)
            raf.readFully(buf)
        }
        var text = String(buf, Charsets.UTF_8)
        if (want < size) {
            val nl = text.indexOf('\n')
            if (nl >= 0) text = text.substring(nl + 1)
        }
        return mapOf("text" to text, "size" to size)
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val ch = MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "net.itak/emu")
        channel = ch
        EventChannel(
            flutterEngine.dartExecutor.binaryMessenger,
            "net.itak/emu/update_progress"
        ).setStreamHandler(object : EventChannel.StreamHandler {
            override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
                updateProgressSink = events
            }

            override fun onCancel(arguments: Any?) {
                updateProgressSink = null
            }
        })
        ch.setMethodCallHandler { call, result ->
                when (call.method) {
                    // SharedPreferences bridge (itak_prefs), shared with
                    // EmulatorActivity: player list and current player.
                    "getPref" -> {
                        val key = call.argument<String>("key")
                        val prefs = getSharedPreferences("itak_prefs", Context.MODE_PRIVATE)
                        result.success(if (key != null) prefs.getString(key, null) else null)
                    }
                    "setPref" -> {
                        val key = call.argument<String>("key")
                        val value = call.argument<String>("value")
                        if (key != null && value != null) {
                            getSharedPreferences("itak_prefs", Context.MODE_PRIVATE)
                                .edit().putString(key, value).apply()
                            result.success(true)
                        } else {
                            result.error("BAD_ARGS", "key and value required", null)
                        }
                    }
                    // Int flavor: EmulatorActivity reads filter/display
                    // modes with getInt; a String stored under those keys
                    // would crash that read.
                    "getPrefInt" -> {
                        val key = call.argument<String>("key")
                        val prefs = getSharedPreferences("itak_prefs", Context.MODE_PRIVATE)
                        result.success(
                            if (key != null && prefs.contains(key)) prefs.getInt(key, 0) else null
                        )
                    }
                    "setPrefInt" -> {
                        val key = call.argument<String>("key")
                        val value = call.argument<Int>("value")
                        if (key != null && value != null) {
                            getSharedPreferences("itak_prefs", Context.MODE_PRIVATE)
                                .edit().putInt(key, value).apply()
                            result.success(true)
                        } else {
                            result.error("BAD_ARGS", "key and value required", null)
                        }
                    }
                    // Controller assignment: list connected gamepads,
                    // or arm the one-shot "press any button" capture.
                    "listControllers" -> {
                        val list = mutableListOf<Map<String, Any>>()
                        for (id in InputDevice.getDeviceIds()) {
                            val dev = InputDevice.getDevice(id) ?: continue
                            val gamepad = dev.sources and InputDevice.SOURCE_GAMEPAD != 0 ||
                                dev.sources and InputDevice.SOURCE_JOYSTICK != 0
                            if (gamepad) {
                                list.add(mapOf("id" to id, "name" to dev.name))
                            }
                        }
                        result.success(list)
                    }
                    "captureController" -> {
                        captureArmed = true
                        result.success(true)
                    }
                    "cancelCapture" -> {
                        captureArmed = false
                        result.success(true)
                    }
                    // One app for every box: Dart asks whether this
                    // device is 64-bit capable; 64-bit-only systems
                    // show locked on 32-bit hardware.
                    "deviceInfo" -> {
                        result.success(
                            mapOf("is64" to android.os.Build.SUPPORTED_64_BIT_ABIS.isNotEmpty())
                        )
                    }
                    // Friend alert chime: Dart calls this when a
                    // friend alert banner shows. Fire and forget;
                    // the chime plays on its own thread and any
                    // failure is silent.
                    "playNotifySound" -> {
                        playNotifyChime()
                        result.success(true)
                    }
                    // Settings -> Sound test: the native speaker and
                    // microphone level-bar screen.
                    "soundTest" -> {
                        startActivity(
                            android.content.Intent(
                                this,
                                SoundTestActivity::class.java
                            )
                        )
                        result.success(true)
                    }
                    // A player rename moves their saves folder and
                    // stats file so nothing is lost.
                    "renamePlayerFiles" -> {
                        val oldName = call.argument<String>("old")
                        val newName = call.argument<String>("new")
                        if (oldName != null && newName != null) {
                            try {
                                val oldSaves = File(filesDir, "saves/$oldName")
                                val newSaves = File(filesDir, "saves/$newName")
                                if (oldSaves.exists() && !newSaves.exists()) {
                                    oldSaves.renameTo(newSaves)
                                }
                                val sanitize = { s: String ->
                                    s.replace(Regex("[^A-Za-z0-9_-]"), "_")
                                }
                                val oldStats = File(filesDir, "stats/${sanitize(oldName)}.json")
                                val newStats = File(filesDir, "stats/${sanitize(newName)}.json")
                                if (oldStats.exists() && !newStats.exists()) {
                                    oldStats.renameTo(newStats)
                                }
                            } catch (e: Exception) { /* best effort */ }
                            result.success(true)
                        } else {
                            result.error("BAD_ARGS", "old and new required", null)
                        }
                    }
                    // Leaderboard: hand Dart the raw per-player stat
                    // files (files/stats/*.json) as one JSON array.
                    "getLeaderboard" -> {
                        val dir = File(filesDir, "stats")
                        val parts = mutableListOf<String>()
                        dir.listFiles { f -> f.name.endsWith(".json") }
                            ?.sortedBy { it.name }
                            ?.forEach { f ->
                                try {
                                    parts.add(f.readText())
                                } catch (e: Exception) { /* skip bad file */ }
                            }
                        result.success("[" + parts.joinToString(",") + "]")
                    }
                    // Self-update: installed version facts for the
                    // update check in Dart.
                    "appInfo" -> {
                        @Suppress("DEPRECATION")
                        val info = packageManager.getPackageInfo(packageName, 0)
                        val code = if (Build.VERSION.SDK_INT >= 28) {
                            info.longVersionCode.toInt()
                        } else {
                            @Suppress("DEPRECATION")
                            info.versionCode
                        }
                        result.success(
                            mapOf(
                                "versionCode" to code,
                                "versionName" to (info.versionName ?: "")
                            )
                        )
                    }
                    // Debug logs (Settings -> Logs). Dart app
                    // events are written into logcat so the
                    // capture thread files them with everything
                    // else.
                    "log" -> {
                        val msg = call.argument<String>("message") ?: ""
                        android.util.Log.i("iTaKApp", msg)
                        result.success(true)
                    }
                    "readLog" -> {
                        thread { result.success(readLogTail()) }
                    }
                    "logInfo" -> {
                        val f = logFile()
                        result.success(
                            mapOf("size" to if (f.exists()) f.length() else 0L)
                        )
                    }
                    "clearLog" -> {
                        thread {
                            try {
                                java.io.FileOutputStream(logFile(), false)
                                    .close()
                                result.success(true)
                            } catch (e: Exception) {
                                result.error("IO", e.message, null)
                            }
                        }
                    }
                    "exportLog" -> {
                        thread {
                            try {
                                val stamp = java.text.SimpleDateFormat(
                                    "yyyyMMdd-HHmmss", java.util.Locale.US
                                ).format(java.util.Date())
                                val dir = File(
                                    android.os.Environment
                                        .getExternalStorageDirectory(), "iTaK"
                                )
                                if (!dir.exists()) dir.mkdirs()
                                val out = File(dir, "itak-log-$stamp.txt")
                                logFile().copyTo(out, overwrite = true)
                                result.success(out.absolutePath)
                            } catch (e: Exception) {
                                result.error("IO", e.message, null)
                            }
                        }
                    }
                    // Self-update: download the APK on a worker
                    // thread, verify its SHA-256, then hand it to
                    // the system installer. Byte progress streams to
                    // Dart over the update_progress event channel.
                    // Returns "" on success, "needs-permission" when
                    // the TV still has to allow installs from this
                    // app (the settings page is opened for the user),
                    // or an error message. Android always shows its
                    // own Install prompt; no sideloaded app can skip
                    // that.
                    "installUpdate" -> {
                        val dlUrl = call.argument<String>("url")
                        val wantSha = call.argument<String>("sha256")
                        val wantSize = call.argument<Number>("size")?.toLong() ?: 0L
                        if (dlUrl == null || wantSha == null) {
                            result.error("BAD_ARGS", "url and sha256 required", null)
                        } else {
                            thread(name = "app-update") {
                                val outcome = try {
                                    downloadAndInstall(dlUrl, wantSha, wantSize)
                                } catch (e: Exception) {
                                    "Download failed: ${e.message ?: "network error"}"
                                }
                                runOnUiThread { result.success(outcome) }
                            }
                        }
                    }
                    "launch" -> {
                        val core = call.argument<String>("core") ?: "libsnes9x.so"
                        val rom = call.argument<String>("rom")
                        if (rom == null) {
                            result.error("NO_ROM", "rom path required", null)
                        } else {
                            val intent = Intent(this, EmulatorActivity::class.java)
                            intent.putExtra(EmulatorActivity.EXTRA_CORE, core)
                            intent.putExtra(EmulatorActivity.EXTRA_ROM, rom)
                            // Optional netplay session (Online screen).
                            call.argument<String>("npRole")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_ROLE, it)
                            }
                            call.argument<String>("npLobby")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_LOBBY, it)
                            }
                            call.argument<String>("npToken")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_TOKEN, it)
                            }
                            call.argument<String>("npServer")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_SERVER, it)
                            }
                            // The lobby password, so a seat
                            // rotation inside the game screen can
                            // re-enter the room (watch or join)
                            // without asking for it again.
                            call.argument<String>("npPassword")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_PASSWORD, it)
                            }
                            // Optional watch mode (spectator from the
                            // Online screen): mode/server/lobby/token
                            // ride the same extras the netplay launch
                            // uses, plus the watcher's player name.
                            call.argument<String>("mode")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_MODE, it)
                            }
                            call.argument<String>("serverBase")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_SERVER, it)
                            }
                            call.argument<String>("lobbyId")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_LOBBY, it)
                            }
                            call.argument<String>("token")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_TOKEN, it)
                            }
                            call.argument<String>("playerName")?.let {
                                intent.putExtra(EmulatorActivity.EXTRA_NP_PLAYER, it)
                            }
                            startActivity(intent)
                            result.success(true)
                        }
                    }
                    else -> result.notImplemented()
                }
            }
    }

    // Downloads an update APK, verifies it, and opens the system
    // installer. Runs on a worker thread; returns "" on success,
    // "needs-permission", or a plain error message for the user.
    // The bytes land in update.apk.part first and are renamed to
    // update.apk only after the size and SHA-256 both check out,
    // so a partial file can never reach the installer.
    private fun downloadAndInstall(
        dlUrl: String,
        wantSha: String,
        wantSize: Long
    ): String {
        val out = File(cacheDir, "update.apk")
        val part = File(cacheDir, "update.apk.part")
        part.delete()
        val digest = MessageDigest.getInstance("SHA-256")
        val conn = URL(dlUrl).openConnection() as HttpURLConnection
        conn.connectTimeout = 15000
        conn.readTimeout = 60000
        conn.instanceFollowRedirects = true
        conn.connect()
        if (conn.responseCode != 200) {
            return "Download failed: server answered ${conn.responseCode}"
        }
        // Progress total: the HTTP length when the server sends one,
        // else the size version.json promised (0 = unknown).
        val httpTotal = conn.contentLengthLong
        val total = if (httpTotal > 0) httpTotal else wantSize
        var downloaded = 0L
        reportUpdateProgress(0L, total)
        try {
            conn.inputStream.use { input ->
                FileOutputStream(part).use { fos ->
                    val buf = ByteArray(64 * 1024)
                    var lastReport = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        fos.write(buf, 0, n)
                        digest.update(buf, 0, n)
                        downloaded += n
                        if (downloaded - lastReport >= 256 * 1024) {
                            lastReport = downloaded
                            reportUpdateProgress(downloaded, total)
                        }
                    }
                }
            }
        } catch (e: Exception) {
            part.delete()
            throw e
        }
        reportUpdateProgress(downloaded, total)
        if (httpTotal > 0 && downloaded != httpTotal) {
            part.delete()
            return "Download failed: the connection dropped. Try again."
        }
        val gotSha = digest.digest().joinToString("") { "%02x".format(it) }
        if (!gotSha.equals(wantSha, ignoreCase = true)) {
            part.delete()
            return "Download failed: the file did not verify. Try again."
        }
        if (!part.renameTo(out)) {
            try {
                part.copyTo(out, overwrite = true)
                part.delete()
            } catch (e: Exception) {
                part.delete()
                return "Download failed: could not save the update file."
            }
        }
        if (Build.VERSION.SDK_INT >= 26 && !packageManager.canRequestPackageInstalls()) {
            // One-time TV setting: allow iTaK Gaming to install
            // updates, then the user taps Update again.
            val settings = Intent(
                Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                Uri.parse("package:$packageName")
            )
            settings.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            startActivity(settings)
            return "needs-permission"
        }
        val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", out)
        val install = Intent(Intent.ACTION_VIEW)
        install.setDataAndType(uri, "application/vnd.android.package-archive")
        install.addFlags(
            Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK
        )
        startActivity(install)
        return ""
    }

    // Soft two-tone chime for friend alerts, generated as sine
    // samples (no audio asset ships with the app): a low tone
    // then a higher one, about 0.35 s total at modest volume,
    // each tone fading in and out so it dings instead of clicks.
    // Plays through the notification stream. Everything is
    // wrapped so a failure can never crash or block the caller.
    private fun playNotifyChime() {
        thread(name = "notify-chime") {
            try {
                val sampleRate = 44100
                val tones = doubleArrayOf(880.0, 1318.5)
                val segSeconds = 0.175
                val framesPerSeg = (sampleRate * segSeconds).toInt()
                val frames = framesPerSeg * tones.size
                val samples = ShortArray(frames)
                val peak = Short.MAX_VALUE * 0.22
                for (seg in tones.indices) {
                    val freq = tones[seg]
                    for (j in 0 until framesPerSeg) {
                        val t = j / sampleRate.toDouble()
                        val attack = Math.min(1.0, t / 0.012)
                        val decay = Math.exp(-2.4 * t / segSeconds)
                        val s = Math.sin(2.0 * Math.PI * freq * t) *
                            attack * decay * peak
                        samples[seg * framesPerSeg + j] = s.toInt().toShort()
                    }
                }
                val track = AudioTrack.Builder()
                    .setAudioAttributes(
                        AudioAttributes.Builder()
                            .setUsage(AudioAttributes.USAGE_NOTIFICATION)
                            .setContentType(
                                AudioAttributes.CONTENT_TYPE_SONIFICATION
                            )
                            .build()
                    )
                    .setAudioFormat(
                        AudioFormat.Builder()
                            .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                            .setSampleRate(sampleRate)
                            .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                            .build()
                    )
                    .setBufferSizeInBytes(samples.size * 2)
                    .setTransferMode(AudioTrack.MODE_STATIC)
                    .build()
                try {
                    track.write(samples, 0, samples.size)
                    track.play()
                    // MODE_STATIC plays out in real time; wait a
                    // little past the end before releasing.
                    Thread.sleep(500)
                } finally {
                    try {
                        track.release()
                    } catch (e: Exception) { /* already gone */ }
                }
            } catch (e: Exception) {
                // Silent by design: an alert sound must never
                // take the app down with it.
            }
        }
    }
}
