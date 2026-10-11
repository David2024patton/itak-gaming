package net.itak.itak_gaming

import android.content.Context
import android.content.Intent
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "net.itak/emu")
            .setMethodCallHandler { call, result ->
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
                    "launch" -> {
                        val core = call.argument<String>("core") ?: "libsnes9x.so"
                        val rom = call.argument<String>("rom")
                        if (rom == null) {
                            result.error("NO_ROM", "rom path required", null)
                        } else {
                            val intent = Intent(this, EmulatorActivity::class.java)
                            intent.putExtra(EmulatorActivity.EXTRA_CORE, core)
                            intent.putExtra(EmulatorActivity.EXTRA_ROM, rom)
                            startActivity(intent)
                            result.success(true)
                        }
                    }
                    else -> result.notImplemented()
                }
            }
    }
}
