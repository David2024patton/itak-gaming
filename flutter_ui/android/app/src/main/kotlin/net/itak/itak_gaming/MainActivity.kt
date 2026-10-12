package net.itak.itak_gaming

import android.content.Context
import android.content.Intent
import android.hardware.input.InputManager
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
import io.flutter.plugin.common.MethodChannel
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import kotlin.concurrent.thread

class MainActivity : FlutterActivity() {

    private var channel: MethodChannel? = null
    private var lastControllerPromptMs = 0L

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
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val ch = MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "net.itak/emu")
        channel = ch
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
                    // Self-update: download the APK on a worker
                    // thread, verify its SHA-256, then hand it to
                    // the system installer. Returns "" on success,
                    // "needs-permission" when the TV still has to
                    // allow installs from this app (the settings
                    // page is opened for the user), or an error
                    // message. Android always shows its own Install
                    // prompt; no sideloaded app can skip that.
                    "installUpdate" -> {
                        val dlUrl = call.argument<String>("url")
                        val wantSha = call.argument<String>("sha256")
                        if (dlUrl == null || wantSha == null) {
                            result.error("BAD_ARGS", "url and sha256 required", null)
                        } else {
                            thread(name = "app-update") {
                                val outcome = try {
                                    downloadAndInstall(dlUrl, wantSha)
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
    private fun downloadAndInstall(dlUrl: String, wantSha: String): String {
        val out = File(cacheDir, "update.apk")
        val digest = MessageDigest.getInstance("SHA-256")
        val conn = URL(dlUrl).openConnection() as HttpURLConnection
        conn.connectTimeout = 15000
        conn.readTimeout = 60000
        conn.instanceFollowRedirects = true
        conn.connect()
        if (conn.responseCode != 200) {
            return "Download failed: server answered ${conn.responseCode}"
        }
        conn.inputStream.use { input ->
            FileOutputStream(out).use { fos ->
                val buf = ByteArray(64 * 1024)
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    fos.write(buf, 0, n)
                    digest.update(buf, 0, n)
                }
            }
        }
        val gotSha = digest.digest().joinToString("") { "%02x".format(it) }
        if (!gotSha.equals(wantSha, ignoreCase = true)) {
            out.delete()
            return "Download failed: the file did not verify. Try again."
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
}
