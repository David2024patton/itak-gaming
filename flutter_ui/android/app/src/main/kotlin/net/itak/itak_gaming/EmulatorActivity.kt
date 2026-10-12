package net.itak.itak_gaming

import android.app.Activity
import android.app.AlertDialog
import android.graphics.Color
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import android.os.Bundle
import android.util.Log
import android.view.KeyEvent
import android.view.WindowManager
import android.widget.Toast
import mobile.Mobile
import java.io.File
import kotlin.concurrent.thread

/**
 * Full-screen game screen. Runs the Go libretro backend on a game thread,
 * renders with OpenGL (EmuGLView), plays audio through AudioTrack on its
 * own thread, and maps remote/gamepad keys to joypad buttons.
 *
 * BACK opens the pause menu: resume, per-game save/load state, picture
 * filter, exit. Emulation pauses while the menu is open.
 */
class EmulatorActivity : Activity(), TextureListener {
    companion object {
        const val EXTRA_CORE = "core"
        const val EXTRA_ROM = "rom"
        private const val TAG = "iTaKEmu"
    }

    private lateinit var glView: EmuGLView
    private var audioTrack: AudioTrack? = null
    @Volatile private var running = false
    @Volatile private var paused = false
    @Volatile private var pauseAck = false

    /** Stereo frames the AudioTrack has accepted; read by the game loop for audio sync. */
    @Volatile private var writtenFrames = 0L
    private var gameThread: Thread? = null
    private var romPath: String = ""
    private var menuOpen = false

    // ----- Local multiplayer seats -----
    // Controller assignments (player name -> deviceId) come from the
    // itak_prefs "controller_map" JSON written by the player picker.
    // Rules: an assigned pad drives only its owner's seat; seat 0
    // belongs to the current player and stays reserved for their pad
    // even while it is disconnected (nobody can steal the position);
    // seat 1 exists only for a second local player with an assigned,
    // connected pad (drop-in). An unassigned pad may fill seat 0 only
    // when the current player has no pad assigned at all. The TV
    // remote is menu/backup control for seat 0 unless the owner's pad
    // is connected and holding the seat.
    private var seatPlayers: List<String> = emptyList()
    private var seatCurrent: String = ""
    private val assignedPadByPlayer = mutableMapOf<String, Int>()
    private val padSeats = mutableMapOf<Int, Int>() // deviceId -> slot
    private val connectedPads = mutableSetOf<Int>()
    private var fallbackPad: Int? = null

    private val seatDeviceListener = object : android.hardware.input.InputManager.InputDeviceListener {
        override fun onInputDeviceAdded(deviceId: Int) {
            if (isGamepadDevice(deviceId)) {
                connectedPads.add(deviceId)
                refreshSeats()
            }
        }
        override fun onInputDeviceRemoved(deviceId: Int) {
            connectedPads.remove(deviceId)
            if (fallbackPad == deviceId) fallbackPad = null
            refreshSeats()
        }
        override fun onInputDeviceChanged(deviceId: Int) {}
    }

    private fun isGamepadDevice(deviceId: Int): Boolean {
        val dev = android.view.InputDevice.getDevice(deviceId) ?: return false
        return dev.sources and android.view.InputDevice.SOURCE_GAMEPAD != 0 ||
            dev.sources and android.view.InputDevice.SOURCE_JOYSTICK != 0
    }

    private fun setupSeats() {
        val prefs = getSharedPreferences("itak_prefs", MODE_PRIVATE)
        seatPlayers = (prefs.getString("players", "") ?: "")
            .split("\n").map { it.trim() }.filter { it.isNotEmpty() }
        seatCurrent = currentPlayer()
        assignedPadByPlayer.clear()
        val mapJson = prefs.getString("controller_map", "") ?: ""
        if (mapJson.isNotEmpty()) {
            try {
                val obj = org.json.JSONObject(mapJson)
                for (key in obj.keys()) {
                    val entry = obj.optJSONObject(key) ?: continue
                    val id = entry.optInt("id", -1)
                    if (id >= 0) assignedPadByPlayer[key] = id
                }
            } catch (e: Exception) {
                Log.w(TAG, "controller_map parse failed", e)
            }
        }
        connectedPads.clear()
        for (id in android.view.InputDevice.getDeviceIds()) {
            if (isGamepadDevice(id)) connectedPads.add(id)
        }
        refreshSeats()
        val im = getSystemService(android.content.Context.INPUT_SERVICE)
            as android.hardware.input.InputManager
        im.registerInputDeviceListener(
            seatDeviceListener,
            android.os.Handler(android.os.Looper.getMainLooper()),
        )
    }

    private fun refreshSeats() {
        padSeats.clear()
        assignedPadByPlayer[seatCurrent]?.let { padSeats[it] = 0 }
        for (p in seatPlayers) {
            if (p == seatCurrent) continue
            val dev = assignedPadByPlayer[p] ?: continue
            // Player 2 is local only, and only when their pad is
            // actually connected (active).
            if (dev in connectedPads) {
                padSeats[dev] = 1
            }
            break
        }
        if (assignedPadByPlayer[seatCurrent] == null && fallbackPad != null) {
            padSeats.putIfAbsent(fallbackPad!!, 0)
        }
        Log.i(TAG, "seats: pads=$padSeats connected=$connectedPads current=$seatCurrent")
    }

    // Which player slot an event drives, or null to ignore it.
    private fun slotFor(event: KeyEvent): Int? {
        val dev = event.device
        val gamepad = dev != null &&
            (dev.sources and android.view.InputDevice.SOURCE_GAMEPAD != 0 ||
                dev.sources and android.view.InputDevice.SOURCE_JOYSTICK != 0)
        val ownerPad = assignedPadByPlayer[seatCurrent]
        if (!gamepad) {
            // Remote / keyboard: seat 0 backup, unless the owner's own
            // pad is connected and holding the seat.
            return if (ownerPad != null && ownerPad in connectedPads) null else 0
        }
        val id = event.deviceId
        padSeats[id]?.let { return it }
        if (ownerPad == null) {
            // No pad assigned to player 1: the first pad to play
            // fills seat 0 for this session; later pads get no seat.
            if (fallbackPad == null) {
                fallbackPad = id
                padSeats[id] = 0
                return 0
            }
            return null
        }
        // Player 1's seat is reserved for their assigned pad.
        return null
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        window.setBackgroundDrawable(android.graphics.drawable.ColorDrawable(Color.BLACK))

        // Restore the saved picture filter and display mode.
        val prefs = getSharedPreferences("itak_prefs", MODE_PRIVATE)
        FilterSettings.mode = prefs.getInt("filter_mode", 0)
        FilterSettings.displayMode = prefs.getInt("display_mode", 0)
        swapAB = prefs.getInt("swap_ab", 0) != 0
        Log.i(TAG, "swap_ab = $swapAB")
        autoSaveEnabled = prefs.getString("autosave", "1") != "0"
        Log.i(TAG, "autosave = $autoSaveEnabled")

        glView = EmuGLView(this)
        setContentView(glView)

        val coreAsset = intent.getStringExtra(EXTRA_CORE) ?: "libsnes9x.so"
        romPath = intent.getStringExtra(EXTRA_ROM) ?: return finish()
        Log.i(TAG, "onCreate: core=$coreAsset rom=$romPath")

        val coreFile = extractCore(coreAsset)
        Log.i(TAG, "core extracted to ${coreFile.absolutePath} size=${coreFile.length()}")
        try {
            Mobile.loadCore(coreFile.absolutePath)
            Mobile.loadGame(romPath)
            Log.i(TAG, "core+game loaded")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to start emulation", e)
            finish()
            return
        }
        // Cheats for this game (bundled list + the codes left on last
        // time). Applied before the first frame runs.
        loadCheatDb()
        if (enabledCheats.isNotEmpty()) applyEnabledCheats()
        setupSeats()
        statsBegin()
        startAudio()
        Log.i(TAG, "onCreate done")
    }

    // TextureListener (from the GL renderer's first surface)
    override fun onSurfaceReady() {
        if (running) return
        running = true
        gameThread = thread(name = "emu-loop") { gameLoop() }
    }

    private fun startAudio() {
        var rate = 0
        try { rate = Mobile.sampleRate().toInt() } catch (e: Exception) { Log.w(TAG, "sampleRate failed", e) }
        if (rate <= 0) rate = 32040
        Log.i(TAG, "audio rate = $rate")
        val minBuf = AudioTrack.getMinBufferSize(
            rate, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT
        )
        audioTrack = AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_GAME)
                    .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                    .build()
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .setSampleRate(rate)
                    .setChannelMask(AudioFormat.CHANNEL_OUT_STEREO)
                    .build()
            )
            .setBufferSizeInBytes(maxOf(minBuf * 2, 16384))
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()
        audioTrack?.play()
    }

    private fun extractCore(assetName: String): File {
        val out = File(filesDir, assetName)
        if (!out.exists() || out.length() == 0L) {
            assets.open("cores/$assetName").use { input ->
                out.outputStream().use { input.copyTo(it) }
            }
        }
        return out
    }

    private fun gameLoop() {
        val fps = Mobile.fps().toDouble()
        val frameNanos = 1_000_000_000.0 / fps
        var frameCount = 0L
        var tRun = 0L
        Log.i(TAG, "gameLoop start fps=$fps")

        // Audio thread: pulls PCM from the backend and feeds the track.
        // Non-blocking writes can accept only part of a chunk; the rest
        // stays in `pending` for the next pass. Dropping the remainder
        // was the crackle/wacky-sound bug.
        val audioThread = thread(name = "emu-audio") {
            var pending = ByteArray(0)
            while (running) {
                if (paused) {
                    Thread.sleep(20)
                    continue
                }
                val chunk = Mobile.drainAudio()
                if (chunk != null && chunk.isNotEmpty()) {
                    pending = if (pending.isEmpty()) chunk else pending + chunk
                    if (pending.size > 131072) {
                        // Runaway guard: keep only the newest half.
                        pending = pending.copyOfRange(pending.size - 65536, pending.size)
                    }
                }
                if (pending.isNotEmpty()) {
                    try {
                        val n = audioTrack?.write(pending, 0, pending.size, AudioTrack.WRITE_NON_BLOCKING) ?: 0
                        if (n > 0) {
                            writtenFrames += n / 4 // 2 channels x 2 bytes per stereo frame
                            pending = if (n >= pending.size) ByteArray(0)
                            else pending.copyOfRange(n, pending.size)
                        }
                        if (n == 0) Thread.sleep(2)
                    } catch (e: Exception) {
                        Log.w(TAG, "audio write failed", e)
                        Thread.sleep(10)
                    }
                } else {
                    Thread.sleep(3)
                }
            }
        }

        var nextDeadline = System.nanoTime().toDouble()
        while (running) {
            if (paused) {
                pauseAck = true
                Thread.sleep(30)
                nextDeadline = System.nanoTime().toDouble()
                continue
            }
            pauseAck = false
            val start = System.nanoTime()
            Mobile.runFrame()
            val elapsed = (System.nanoTime() - start) / 1_000_000
            tRun += elapsed
            frameCount++
            // Steady 30 draws every other frame; the core still runs
            // (and sounds) at full speed.
            if (FilterSettings.displayMode == 0 || frameCount % 2L == 0L) {
                glView.requestRender()
            }
            if (frameCount % 600 == 0L) {
                Log.i(TAG, "emu frame=$frameCount avg run ms=${tRun / 600}")
                tRun = 0
            }
            // Auto-save once a minute into the player's save slot, so
            // progress survives even if nobody remembers to save. Runs
            // here on the game thread, between frames, never racing
            // the core.
            if (autoSaveEnabled &&
                System.currentTimeMillis() - lastAutoSaveMs > 60_000) {
                lastAutoSaveMs = System.currentTimeMillis()
                try {
                    val data = Mobile.saveState()
                    if (data != null && data.isNotEmpty()) {
                        saveFile().writeBytes(data)
                        Log.i(TAG, "auto-saved ${data.size} bytes")
                    }
                } catch (e: Exception) {
                    Log.w(TAG, "auto-save failed", e)
                }
            }
            // Deadline pacing: carry the fractional part of the frame
            // interval so the long-run rate is exactly the core's fps.
            // A plain sleep(frameMs) truncated 16.64ms to 16ms and only
            // sleep() overshoot kept the rate near 60.
            nextDeadline += frameNanos
            var waitNs = (nextDeadline - System.nanoTime()).toLong()
            if (waitNs < -50_000_000L) {
                // Fell far behind (long pause, hiccup): resync.
                nextDeadline = System.nanoTime().toDouble()
                waitNs = 0
            }
            if (waitNs > 0) {
                Thread.sleep(waitNs / 1_000_000, (waitNs % 1_000_000).toInt())
            }
            // Audio gate: the soundtrack is the master clock. If too
            // much audio is queued ahead of what the DAC has played,
            // wait instead of producing more. Without this the core
            // and the sound card drift apart by fractions of a percent
            // until the buffer starves (crackle) or floods (skips).
            val track = audioTrack
            if (track != null) {
                var guard = 0
                while (running && !paused && guard < 12) {
                    val played = track.playbackHeadPosition.toLong() and 0xFFFFFFFFL
                    if (writtenFrames - played <= 2400) break
                    Thread.sleep(1)
                    guard++
                }
            }
        }
        audioThread.join(500)
    }

    // ---------- Pause menu ----------

    /** Who is playing, picked on the Flutter home screen. */
    private fun currentPlayer(): String {
        val p = getSharedPreferences("itak_prefs", MODE_PRIVATE)
            .getString("current_player", null)
        return p?.takeIf { it.isNotBlank() } ?: "Player 1"
    }

    private fun romBase(): String =
        File(romPath).nameWithoutExtension.replace(Regex("[^A-Za-z0-9._-]"), "_")

    /** Per-player save slot: saves/<player>/<game>.sav */
    private fun saveFile(): File {
        val player = currentPlayer().replace(Regex("[^A-Za-z0-9._-]"), "_")
        val dir = File(filesDir, "saves/$player")
        if (!dir.exists()) dir.mkdirs()
        return File(dir, "${romBase()}.sav")
    }

    /** Saves written before player profiles lived here: saves/<game>.sav */
    private fun legacySaveFile(): File = File(filesDir, "saves/${romBase()}.sav")

    // ---------------------------------------------------------------
    // Play stats for the leaderboard: per player, per game: launches,
    // seconds played, last-played time. files/stats/<player>.json
    // ---------------------------------------------------------------
    private var swapAB = false
    private var autoSaveEnabled = true
    @Volatile private var lastAutoSaveMs = System.currentTimeMillis()
    private var statsArmed = false
    private var sessionStartMs = 0L
    private var statsPlayer = ""
    private var statsObj: org.json.JSONObject? = null

    private fun statsFile(player: String): File {
        val dir = File(filesDir, "stats")
        if (!dir.exists()) dir.mkdirs()
        val safe = player.replace(Regex("[^A-Za-z0-9_-]"), "_")
        return File(dir, "$safe.json")
    }

    private fun statsBegin() {
        try {
            statsPlayer = currentPlayer()
            val f = statsFile(statsPlayer)
            val obj = if (f.exists()) {
                org.json.JSONObject(f.readText())
            } else {
                org.json.JSONObject()
            }
            obj.put("name", statsPlayer)
            if (!obj.has("games")) obj.put("games", org.json.JSONObject())
            val games = obj.getJSONObject("games")
            val g = if (games.has(romPath)) {
                games.getJSONObject(romPath)
            } else {
                org.json.JSONObject()
            }
            g.put("plays", g.optInt("plays", 0) + 1)
            if (!g.has("seconds")) g.put("seconds", 0)
            g.put("last", System.currentTimeMillis())
            games.put(romPath, g)
            f.writeText(obj.toString())
            statsObj = obj
            sessionStartMs = System.currentTimeMillis()
            statsArmed = true
            Log.i(TAG, "stats: play #${g.getInt("plays")} of $romPath for $statsPlayer")
        } catch (e: Exception) {
            Log.w(TAG, "stats begin failed", e)
        }
    }

    private fun statsEnd() {
        if (!statsArmed) return
        statsArmed = false
        try {
            val obj = statsObj ?: return
            val secs = ((System.currentTimeMillis() - sessionStartMs) / 1000).toInt()
            val games = obj.getJSONObject("games")
            val g = games.getJSONObject(romPath)
            g.put("seconds", g.optInt("seconds", 0) + secs)
            g.put("last", System.currentTimeMillis())
            statsFile(statsPlayer).writeText(obj.toString())
            Log.i(TAG, "stats: +${secs}s for $statsPlayer")
        } catch (e: Exception) {
            Log.w(TAG, "stats end failed", e)
        }
    }

    private fun showPauseMenu() {
        if (menuOpen) return
        menuOpen = true
        paused = true
        // Wait until the game thread is actually parked between frames so
        // save/load calls never race an in-flight frame.
        var waited = 0
        while (!pauseAck && waited < 200) {
            Thread.sleep(5)
            waited += 5
        }
        try { audioTrack?.pause() } catch (e: Exception) { /* ignore */ }

        val filterName = FilterSettings.NAMES[FilterSettings.mode]
        val displayName = FilterSettings.DISPLAY_NAMES[FilterSettings.displayMode]
        val cheatLabel =
            if (enabledCheats.isEmpty()) "Cheats" else "Cheats: ${enabledCheats.size} on"
        val items = arrayOf(
            "Resume",
            "Save Game",
            "Load Game",
            cheatLabel,
            "Picture: $filterName",
            "Display: $displayName",
            "Exit Game"
        )
        // Note: setMessage + setItems together renders an empty list on
        // this TV's dialog theme, so the player rides in the title.
        AlertDialog.Builder(this)
            .setTitle("${File(romPath).nameWithoutExtension}  ·  ${currentPlayer()}")
            .setItems(items) { _, which ->
                when (which) {
                    0 -> Unit // resume via dismiss
                    1 -> doSave()
                    2 -> doLoad()
                    3 -> {
                        // Keep the game parked while the cheat picker
                        // is open; its own dismiss resumes play.
                        keepPaused = true
                        window.decorView.postDelayed({ showCheats() }, 200)
                    }
                    4 -> cycleFilter()
                    5 -> toggleDisplay()
                    6 -> {
                        // Exit asks for confirmation first; the game
                        // stays parked while the prompt is open.
                        keepPaused = true
                        window.decorView.postDelayed({ confirmExit() }, 200)
                    }
                }
            }
            .setOnDismissListener {
                if (keepPaused) {
                    keepPaused = false
                } else {
                    resumeFromMenu()
                }
            }
            .show()
    }

    private fun resumeFromMenu() {
        menuOpen = false
        paused = false
        try { audioTrack?.play() } catch (e: Exception) { /* ignore */ }
    }

    // Exit asks first, so a stray press cannot end a game by
    // accident. Confirming auto-saves (when enabled) on the way out.
    private fun confirmExit() {
        AlertDialog.Builder(this)
            .setTitle("Exit game?")
            .setMessage(
                if (autoSaveEnabled)
                    "Your game auto-saves on the way out, so you can pick up right where you left off."
                else
                    "Anything you have not saved will be lost."
            )
            .setPositiveButton("Exit") { _, _ ->
                autoSaveNow()
                finish()
            }
            .setNegativeButton("Keep playing", null)
            .setOnDismissListener { if (!isFinishing) resumeFromMenu() }
            .show()
    }

    // Save right now, from a parked game (pause menu showing).
    private fun autoSaveNow() {
        if (!autoSaveEnabled) return
        try {
            val data = Mobile.saveState()
            if (data != null && data.isNotEmpty()) {
                saveFile().writeBytes(data)
                Log.i(TAG, "auto-saved on exit (${data.size} bytes)")
            }
        } catch (e: Exception) {
            Log.w(TAG, "exit auto-save failed", e)
        }
    }

    // ---------------------------------------------------------------
    // Cheats: bundled per-game lists (assets/cheats/<game>.json from
    // the libretro cheat database), toggles persist per game in
    // files/cheats/<game>.txt, one code per line.
    // ---------------------------------------------------------------

    private data class Cheat(val desc: String, val code: String)

    private var cheatDb: List<Cheat> = emptyList()
    private var enabledCheats: MutableSet<String> = mutableSetOf()
    private var keepPaused = false

    private fun cheatFile(): File {
        val dir = File(filesDir, "cheats")
        if (!dir.exists()) dir.mkdirs()
        return File(dir, "${romBase()}.txt")
    }

    private fun loadCheatDb() {
        val name = File(romPath).nameWithoutExtension
        cheatDb = try {
            val text = assets.open("cheats/$name.json").bufferedReader().readText()
            val arr = org.json.JSONArray(text)
            List(arr.length()) { i ->
                val o = arr.getJSONObject(i)
                Cheat(o.getString("desc"), o.getString("code"))
            }
        } catch (e: Exception) {
            emptyList()
        }
        enabledCheats = try {
            val f = cheatFile()
            if (f.exists()) {
                f.readLines().map { it.trim() }
                    .filter { it.isNotEmpty() }
                    .toMutableSet()
            } else {
                mutableSetOf()
            }
        } catch (e: Exception) {
            mutableSetOf()
        }
        Log.i(TAG, "cheats: ${cheatDb.size} available, ${enabledCheats.size} enabled")
    }

    private fun persistCheats() {
        try {
            cheatFile().writeText(enabledCheats.joinToString("\n"))
        } catch (e: Exception) {
            Log.e(TAG, "cheat save failed", e)
        }
    }

    private fun applyEnabledCheats() {
        if (!Mobile.cheatsSupported()) {
            Log.i(TAG, "core does not support cheats")
            return
        }
        if (enabledCheats.isEmpty()) {
            Mobile.clearCheats()
            Log.i(TAG, "cheats cleared")
            return
        }
        val n = Mobile.applyCheats(enabledCheats.joinToString("\n"))
        Log.i(TAG, "applied $n cheat entries from ${enabledCheats.size} codes")
    }

    private fun showCheats() {
        if (cheatDb.isEmpty()) {
            Toast.makeText(this, "No cheats for this game yet", Toast.LENGTH_LONG).show()
            resumeFromMenu()
            return
        }
        val labels = cheatDb.map { it.desc }.toTypedArray()
        val working = BooleanArray(cheatDb.size) { enabledCheats.contains(cheatDb[it].code) }
        AlertDialog.Builder(this)
            .setTitle("Cheats")
            .setMultiChoiceItems(labels, working) { _, which, isChecked ->
                working[which] = isChecked
            }
            .setPositiveButton("Done") { _, _ ->
                enabledCheats = cheatDb
                    .filterIndexed { i, _ -> working[i] }
                    .map { it.code }
                    .toMutableSet()
                persistCheats()
                applyEnabledCheats()
                Toast.makeText(
                    this,
                    if (enabledCheats.isEmpty()) "Cheats off"
                    else "${enabledCheats.size} cheat(s) on",
                    Toast.LENGTH_SHORT
                ).show()
            }
            .setNegativeButton("Cancel", null)
            .setOnDismissListener { resumeFromMenu() }
            .show()
    }

    private fun doSave() {
        val data = Mobile.saveState()
        if (data == null || data.isEmpty()) {
            Toast.makeText(this, "Save failed", Toast.LENGTH_SHORT).show()
            return
        }
        saveFile().writeBytes(data)
        Toast.makeText(this, "Game saved", Toast.LENGTH_SHORT).show()
        Log.i(TAG, "saved ${data.size} bytes to ${saveFile().absolutePath}")
    }

    private fun doLoad() {
        // Prefer this player's slot; fall back to a pre-profiles save.
        var f = saveFile()
        if (!f.exists() && legacySaveFile().exists()) f = legacySaveFile()
        if (!f.exists()) {
            Toast.makeText(this, "No saved game for ${currentPlayer()} yet", Toast.LENGTH_SHORT).show()
            return
        }
        try {
            Mobile.loadState(f.readBytes())
            Toast.makeText(this, "Game loaded", Toast.LENGTH_SHORT).show()
        } catch (e: Exception) {
            Log.e(TAG, "load failed", e)
            Toast.makeText(this, "Load failed", Toast.LENGTH_SHORT).show()
        }
    }

    private fun cycleFilter() {
        FilterSettings.mode = (FilterSettings.mode + 1) % FilterSettings.NAMES.size
        getSharedPreferences("itak_prefs", MODE_PRIVATE)
            .edit().putInt("filter_mode", FilterSettings.mode).apply()
        Toast.makeText(this, "Picture: ${FilterSettings.NAMES[FilterSettings.mode]}", Toast.LENGTH_SHORT).show()
    }

    private fun toggleDisplay() {
        FilterSettings.displayMode = (FilterSettings.displayMode + 1) % FilterSettings.DISPLAY_NAMES.size
        getSharedPreferences("itak_prefs", MODE_PRIVATE)
            .edit().putInt("display_mode", FilterSettings.displayMode).apply()
        Log.i(TAG, "display mode = ${FilterSettings.DISPLAY_NAMES[FilterSettings.displayMode]}")
        Toast.makeText(this, "Display: ${FilterSettings.DISPLAY_NAMES[FilterSettings.displayMode]}", Toast.LENGTH_SHORT).show()
    }

    // ---------- Keys ----------

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            if (!menuOpen) showPauseMenu()
            return true
        }
        val btn = mapButton(keyCode)
        if (btn != null) {
            val slot = slotFor(event)
            if (slot != null) {
                Mobile.setButton(slot.toLong(), btn, true)
            }
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onKeyUp(keyCode: Int, event: KeyEvent): Boolean {
        val btn = mapButton(keyCode)
        if (btn != null) {
            val slot = slotFor(event)
            if (slot != null) {
                Mobile.setButton(slot.toLong(), btn, false)
            }
            return true
        }
        return super.onKeyUp(keyCode, event)
    }

    override fun onDestroy() {
        try {
            val im = getSystemService(android.content.Context.INPUT_SERVICE)
                as android.hardware.input.InputManager
            im.unregisterInputDeviceListener(seatDeviceListener)
        } catch (e: Exception) { /* already gone */ }
        statsEnd()
        running = false
        gameThread?.join(1000)
        audioTrack?.stop()
        audioTrack?.release()
        audioTrack = null
        try { Mobile.close() } catch (e: Exception) { Log.w(TAG, "close failed", e) }
        super.onDestroy()
    }

    private fun mapButton(keyCode: Int): Long? {
        val raw = mapButtonRaw(keyCode) ?: return null
        // Settings option: swap the two action buttons everywhere
        // (controller face buttons and the remote's center/mic pair).
        if (swapAB) {
            return when (raw) {
                Mobile.ButtonA -> Mobile.ButtonB
                Mobile.ButtonB -> Mobile.ButtonA
                else -> raw
            }
        }
        return raw
    }

    private fun mapButtonRaw(keyCode: Int): Long? {
        return when (keyCode) {
            KeyEvent.KEYCODE_DPAD_UP -> Mobile.ButtonUp
            KeyEvent.KEYCODE_DPAD_DOWN -> Mobile.ButtonDown
            KeyEvent.KEYCODE_DPAD_LEFT -> Mobile.ButtonLeft
            KeyEvent.KEYCODE_DPAD_RIGHT -> Mobile.ButtonRight
            KeyEvent.KEYCODE_BUTTON_A -> Mobile.ButtonB       // Xbox A (bottom) = SNES B
            KeyEvent.KEYCODE_BUTTON_B -> Mobile.ButtonA       // Xbox B (right) = SNES A
            KeyEvent.KEYCODE_BUTTON_X -> Mobile.ButtonY
            KeyEvent.KEYCODE_BUTTON_Y -> Mobile.ButtonX
            KeyEvent.KEYCODE_BUTTON_L1 -> Mobile.ButtonL
            KeyEvent.KEYCODE_BUTTON_R1 -> Mobile.ButtonR
            KeyEvent.KEYCODE_BUTTON_START, KeyEvent.KEYCODE_MENU -> Mobile.ButtonStart
            KeyEvent.KEYCODE_BUTTON_SELECT -> Mobile.ButtonSelect
            // TV remote play: the big center button is the main action
            // button (B, jump in Mario), the mic/assistant button is A.
            KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER -> Mobile.ButtonB
            KeyEvent.KEYCODE_ASSIST, KeyEvent.KEYCODE_VOICE_ASSIST -> Mobile.ButtonA
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE -> Mobile.ButtonStart
            else -> null
        }
    }
}
