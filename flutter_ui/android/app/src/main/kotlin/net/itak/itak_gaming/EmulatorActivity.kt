package net.itak.itak_gaming

import android.app.Activity
import android.app.AlertDialog
import android.graphics.Color
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.WindowManager
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
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
        const val EXTRA_NP_ROLE = "np_role"
        const val EXTRA_NP_LOBBY = "np_lobby"
        const val EXTRA_NP_TOKEN = "np_token"
        const val EXTRA_NP_SERVER = "np_server"
        const val EXTRA_NP_MODE = "np_mode"
        const val EXTRA_NP_PLAYER = "np_player"
        private const val TAG = "iTaKEmu"

        // Global leaderboard server. Must match kServerBase in
        // flutter_ui/lib/main.dart.
        private const val SERVER_BASE = "https://arcade.itak.live"
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

    // ----- Netplay (online session) -----
    // Set from intent extras when this game was launched from the
    // Online screen. While a session runs, the game thread steps the
    // lockstep engine with localMask instead of running the core
    // solo; local saves and cheats are disabled (they would desync
    // the pair instantly).
    private var npRole: String = ""
    private var npLobby: String = ""
    private var npToken: String = ""
    private var npServer: String = ""
    @Volatile private var npReady = false
    @Volatile private var npFailed: String? = null
    @Volatile private var npCancelled = false
    @Volatile private var localMask = 0
    private val netplay: Boolean get() = npRole.isNotEmpty()

    // ----- Watch mode (spectator) -----
    // Launched from the Online screen's Watch action with its own
    // extras: no seat, no local input. The same core and game load
    // locally, and the Go watch session reconstructs the match
    // from both players' input streams: NetplayWatchStep applies
    // both masks and runs the frame inside the engine, so this
    // screen never touches buttons or calls runFrame itself.
    private var npMode: String = ""
    private var npPlayer: String = ""
    private val watching: Boolean get() = npMode == "watch"
    @Volatile private var watchStarted = false
    @Volatile private var watchFailed: String? = null
    private var watchBanner: TextView? = null
    private var watchWaitLine: TextView? = null
    private var watchEndShown = false
    private val watchTicker = object : Runnable {
        override fun run() {
            if (!watching || isFinishing) return
            updateWatchBanner()
            uiHandler.postDelayed(this, 300)
        }
    }

    // ----- Waiting room overlay (online games) -----
    // Shown over the (still black) game view until the room's
    // ready ritual and countdown finish and frames start.
    private var roomOverlay: FrameLayout? = null
    private var roomPeerLine: TextView? = null
    private var roomLocalLine: TextView? = null
    private var roomStatusLine: TextView? = null
    private var roomCountLine: TextView? = null
    private var roomPanel: LinearLayout? = null
    private var readyButton: Button? = null
    private var myName: String = ""
    private var rootFrameRef: FrameLayout? = null

    // ----- Netplay text chat -----
    // Two surfaces over the one session chat log in the Go
    // engine: a box built into the waiting room panel below, and
    // a full screen panel reachable from the pause menu. Each
    // keeps its own read cursor into the log.
    private var roomChat: ChatBox? = null
    private var pauseChat: ChatBox? = null
    private var chatOverlay: FrameLayout? = null
    private val chatTicker = object : Runnable {
        override fun run() {
            if (chatOverlay?.visibility != View.VISIBLE || isFinishing) return
            pauseChat?.poll()
            uiHandler.postDelayed(this, 300)
        }
    }
    private val uiHandler = Handler(Looper.getMainLooper())
    private val roomTicker = object : Runnable {
        override fun run() {
            if (!netplay || npReady || npFailed != null || isFinishing) {
                hideRoom()
                return
            }
            updateRoom()
            uiHandler.postDelayed(this, 200)
        }
    }

    // "Other player paused" banner: while the peer sits in their
    // menu the game stalls; the session survives (the engine
    // treats a signaled pause as stalled, not lost).
    private var pauseBanner: TextView? = null
    private val pauseTicker = object : Runnable {
        override fun run() {
            if (!netplay || isFinishing) {
                pauseBanner?.visibility = View.GONE
                return
            }
            // Host duty: spectators ask for a snapshot when they
            // join mid-match or fall behind (watch_sync). The poll
            // hands each waiting name out exactly once; guests
            // never answer. Best effort: failures stay silent.
            if (npReady && npRole == "host") {
                try {
                    var guard = 0
                    while (guard < 4) {
                        val name = Mobile.netplayPollWatchSync()
                        if (name.isEmpty()) break
                        Mobile.netplayAnswerWatchSync(name)
                        guard++
                    }
                } catch (e: Exception) { /* silent */ }
            }
            val banner = pauseBanner
            if (banner != null) {
                val peerPaused = npReady &&
                    (try { Mobile.netplayPeerPaused() } catch (e: Exception) { false })
                if (peerPaused) {
                    val name = try { Mobile.netplayPeerName() } catch (e: Exception) { "" }
                    banner.text = "⏸ ${if (name.isNotEmpty()) name else "The other player"} " +
                        "paused — the game waits for them"
                    banner.visibility = View.VISIBLE
                } else {
                    banner.visibility = View.GONE
                }
            }
            uiHandler.postDelayed(this, 300)
        }
    }

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
        val rootFrame = FrameLayout(this)
        rootFrameRef = rootFrame
        rootFrame.addView(
            glView,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT
            )
        )
        setContentView(rootFrame)

        val coreAsset = intent.getStringExtra(EXTRA_CORE) ?: "libsnes9x.so"
        romPath = intent.getStringExtra(EXTRA_ROM) ?: return finish()
        Log.i(TAG, "onCreate: core=$coreAsset rom=$romPath")

        val coreFile = extractCore(coreAsset)
        Log.i(TAG, "core extracted to ${coreFile.absolutePath} size=${coreFile.length()}")
        try {
            // CD cores (PS1, Sega CD) need a system folder for the
            // BIOS files the player supplies. /sdcard/iTaK/system
            // sits next to the systems art folder and is reachable
            // with any file manager.
            val sysDir = java.io.File(
                android.os.Environment.getExternalStorageDirectory(),
                "iTaK/system"
            )
            sysDir.mkdirs()
            Mobile.setSystemDir(sysDir.absolutePath)
            Mobile.loadCore(coreFile.absolutePath)
            Mobile.loadGame(romPath)
            Log.i(TAG, "core+game loaded")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to start emulation", e)
            finish()
            return
        }
        // Netplay launch? The Online screen passes role/lobby/token.
        npRole = intent.getStringExtra(EXTRA_NP_ROLE) ?: ""
        npLobby = intent.getStringExtra(EXTRA_NP_LOBBY) ?: ""
        npToken = intent.getStringExtra(EXTRA_NP_TOKEN) ?: ""
        npServer = intent.getStringExtra(EXTRA_NP_SERVER) ?: ""
        npMode = intent.getStringExtra(EXTRA_NP_MODE) ?: ""
        npPlayer = intent.getStringExtra(EXTRA_NP_PLAYER) ?: ""
        if (netplay) {
            Log.i(TAG, "netplay role=$npRole lobby=$npLobby")
            myName = currentPlayer()
            buildRoomOverlay(rootFrame)
            uiHandler.post(roomTicker)
            uiHandler.post(pauseTicker)
            thread(name = "netplay-connect") {
                val err = Mobile.netplayStart(
                    npServer, npLobby, npToken,
                    java.io.File(romPath).name, npRole == "host", myName
                )
                if (err.isEmpty()) {
                    npReady = true
                    Log.i(TAG, "netplay session ready")
                } else {
                    npFailed = err
                    Log.w(TAG, "netplay start failed: $err")
                }
            }
        }
        // Watch launch? Same screen, spectator extras. The game
        // above is already loaded; starting the watch joins the
        // room and the game loop reconstructs the match from the
        // two input streams.
        if (watching) {
            Log.i(TAG, "watch lobby=$npLobby")
            myName = npPlayer.ifEmpty { currentPlayer() }
            buildWatchBanner(rootFrame)
            uiHandler.post(watchTicker)
            thread(name = "watch-connect") {
                val err = Mobile.netplayStartWatch(
                    npServer, npLobby, npToken,
                    java.io.File(romPath).name, myName
                )
                if (err.isEmpty()) {
                    watchStarted = true
                    Log.i(TAG, "watch session started")
                } else {
                    watchFailed = err
                    Log.w(TAG, "watch start failed: $err")
                }
            }
        }
        // Cheats for this game (bundled list + the codes left on last
        // time). Applied before the first frame runs. Never in an
        // online session or while watching: every side must run the
        // identical game.
        if (!netplay && !watching) {
            loadCheatDb()
            if (enabledCheats.isNotEmpty()) applyEnabledCheats()
        }
        setupSeats()
        statsBegin()
        startAudio()
        Log.i(TAG, "onCreate done")
    }

    // ----- Waiting room UI -----

    private fun roomText(text: String, size: Float, bold: Boolean = false): TextView {
        val tv = TextView(this)
        tv.text = text
        tv.textSize = size
        tv.setTextColor(Color.WHITE)
        if (bold) tv.setTypeface(null, android.graphics.Typeface.BOLD)
        tv.gravity = Gravity.CENTER
        tv.layoutParams = LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT,
            LinearLayout.LayoutParams.WRAP_CONTENT
        )
        return tv
    }

    private fun buildRoomOverlay(root: FrameLayout) {
        val overlay = FrameLayout(this)
        overlay.setBackgroundColor(Color.argb(235, 14, 14, 24))
        val panel = LinearLayout(this)
        panel.orientation = LinearLayout.VERTICAL
        panel.gravity = Gravity.CENTER
        panel.setPadding(64, 48, 64, 48)
        // Fixed panel width: WRAP_CONTENT let long single lines
        // push the panel off-center and clip (build 35 bug).
        val panelParams = FrameLayout.LayoutParams(
            920, FrameLayout.LayoutParams.WRAP_CONTENT
        )
        panelParams.gravity = Gravity.CENTER

        panel.addView(roomText("Online Game", 36f, true))
        val gameName = java.io.File(romPath).name.removeSuffix(".sfc")
            .removeSuffix(".nes").removeSuffix(".smc")
        val sub = roomText("$gameName  ·  Lobby $npLobby", 19f)
        sub.setTextColor(Color.argb(180, 255, 255, 255))
        panel.addView(sub)
        val spacer1 = View(this)
        panel.addView(spacer1, LinearLayout.LayoutParams(1, 36))

        roomLocalLine = roomText("", 24f, true)
        panel.addView(roomLocalLine)
        val spacer2 = View(this)
        panel.addView(spacer2, LinearLayout.LayoutParams(1, 14))
        roomPeerLine = roomText("", 24f)
        panel.addView(roomPeerLine)
        val spacer3 = View(this)
        panel.addView(spacer3, LinearLayout.LayoutParams(1, 30))

        val btn = Button(this)
        btn.text = "READY"
        btn.textSize = 24f
        btn.isFocusable = true
        btn.setOnClickListener {
            Mobile.netplaySetReady()
            updateRoom()
        }
        readyButton = btn
        panel.addView(btn, LinearLayout.LayoutParams(360, 96))
        val spacer4 = View(this)
        panel.addView(spacer4, LinearLayout.LayoutParams(1, 22))

        roomStatusLine = roomText("", 18f)
        roomStatusLine?.setTextColor(Color.argb(170, 255, 255, 255))
        panel.addView(roomStatusLine)

        val spacer5 = View(this)
        panel.addView(spacer5, LinearLayout.LayoutParams(1, 26))
        panel.addView(roomText("Chat", 20f, true))
        val box = ChatBox()
        roomChat = box
        panel.addView(
            box.root,
            LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 300)
        )
        roomPanel = panel
        overlay.addView(panel, panelParams)

        roomCountLine = roomText("", 110f, true)
        roomCountLine?.visibility = View.GONE
        val countParams = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT
        )
        countParams.gravity = Gravity.CENTER
        overlay.addView(roomCountLine, countParams)

        root.addView(
            overlay,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT
            )
        )
        roomOverlay = overlay

        val banner = TextView(this)
        banner.textSize = 20f
        banner.setTextColor(Color.WHITE)
        banner.setBackgroundColor(Color.argb(205, 20, 20, 32))
        banner.setPadding(36, 18, 36, 18)
        banner.gravity = Gravity.CENTER
        banner.visibility = View.GONE
        val bannerParams = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT
        )
        bannerParams.gravity = Gravity.TOP or Gravity.CENTER_HORIZONTAL
        bannerParams.topMargin = 28
        root.addView(banner, bannerParams)
        pauseBanner = banner

        updateRoom()
    }

    private fun updateRoom() {
        val phase = try { Mobile.netplayPhase() } catch (e: Exception) { "waiting" }
        val peer = try { Mobile.netplayPeerName() } catch (e: Exception) { "" }
        val peerReady = try { Mobile.netplayPeerReady() } catch (e: Exception) { false }
        val localReady = try { Mobile.netplayLocalReady() } catch (e: Exception) { false }
        val roleWord = if (npRole == "host") "Host" else "Guest"
        roomLocalLine?.text =
            "● $myName (You) · $roleWord" + if (localReady) " · Ready ✓" else ""
        when (phase) {
            "countdown" -> {
                val n = try { Mobile.netplayCountdown().toInt() } catch (e: Exception) { 0 }
                roomPanel?.visibility = View.GONE
                roomCountLine?.visibility = View.VISIBLE
                roomCountLine?.text = if (n > 0) n.toString() else "GO!"
            }
            "room" -> {
                roomPeerLine?.text =
                    "● $peer" + if (peerReady) " · Ready ✓" else " · not ready yet"
                val btn = readyButton
                if (btn != null) {
                    if (localReady) {
                        btn.text = "READY ✓"
                        btn.isEnabled = false
                        roomStatusLine?.text = "Waiting for $peer to ready up…"
                    } else {
                        btn.text = "READY"
                        btn.isEnabled = true
                        // Only claim focus when nothing in the room
                        // has it: a player down in the chat box must
                        // not be yanked back up here every tick.
                        if (!btn.hasFocus() && roomOverlay?.findFocus() == null) {
                            btn.requestFocus()
                        }
                        roomStatusLine?.text =
                            if (peerReady) "$peer is ready. Your turn!" else "Both players press READY, then the game starts in 5 seconds."
                    }
                }
            }
            else -> { // waiting / connecting
                roomPeerLine?.text = "○ Waiting for the other player to join…"
                readyButton?.let {
                    it.text = "READY"
                    it.isEnabled = false
                }
                roomStatusLine?.text =
                    "Guests pick this lobby on the Online screen. BACK leaves."
            }
        }
        roomChat?.poll()
    }

    private fun hideRoom() {
        roomOverlay?.visibility = View.GONE
    }

    // ----- Watch mode UI -----

    /** The WATCHING banner (top) and the waiting line (center). */
    private fun buildWatchBanner(root: FrameLayout) {
        val banner = TextView(this)
        banner.text = "WATCHING"
        banner.textSize = 20f
        banner.setTypeface(null, android.graphics.Typeface.BOLD)
        banner.setTextColor(Color.WHITE)
        banner.setBackgroundColor(Color.argb(205, 20, 20, 32))
        banner.setPadding(36, 18, 36, 18)
        banner.gravity = Gravity.CENTER
        val bannerParams = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT
        )
        bannerParams.gravity = Gravity.TOP or Gravity.CENTER_HORIZONTAL
        bannerParams.topMargin = 28
        root.addView(banner, bannerParams)
        watchBanner = banner

        val wait = TextView(this)
        wait.text = "Waiting for the match to start"
        wait.textSize = 28f
        wait.setTypeface(null, android.graphics.Typeface.BOLD)
        wait.setTextColor(Color.WHITE)
        wait.gravity = Gravity.CENTER
        val waitParams = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT
        )
        waitParams.gravity = Gravity.CENTER
        root.addView(wait, waitParams)
        watchWaitLine = wait
    }

    /** Poll the watch state: banner names, waiting line, the end. */
    private fun updateWatchBanner() {
        val raw = try { Mobile.netplayWatchInfo() } catch (e: Exception) { return }
        val obj = try { org.json.JSONObject(raw) } catch (e: Exception) { return }
        val phase = obj.optString("phase", "waiting")
        val host = obj.optString("host", "")
        val guest = obj.optString("guest", "")
        watchBanner?.text =
            if (host.isNotEmpty() && guest.isNotEmpty()) {
                "WATCHING  ·  $host vs $guest"
            } else {
                "WATCHING"
            }
        val waiting = phase != "playing" && phase != "ended"
        watchWaitLine?.visibility = if (waiting) View.VISIBLE else View.GONE
        if (waiting) {
            watchWaitLine?.text =
                if (host.isNotEmpty() && guest.isNotEmpty()) {
                    "Waiting for the match to start\n$host vs $guest"
                } else {
                    "Waiting for the match to start"
                }
        }
        if (phase == "ended" && !watchEndShown) {
            watchEndShown = true
            val err = obj.optString("error", "")
            AlertDialog.Builder(this)
                .setTitle("Watch ended")
                .setMessage(if (err.isNotEmpty()) err else "The match is over.")
                .setPositiveButton("OK") { _, _ -> finish() }
                .setOnDismissListener { finish() }
                .show()
        }
    }

    /**
     * One watch tick on the game thread. The engine applies both
     * players' masks and runs the frame itself when the next
     * frame's inputs are both in; the p1/p2 in the reply are
     * informational only, so there is nothing to re-apply here.
     * Returns true when a frame actually ran.
     */
    private fun watchStepOnce(): Boolean {
        val raw = try { Mobile.netplayWatchStep() } catch (e: Exception) {
            return false
        }
        return try {
            org.json.JSONObject(raw).optBoolean("ok", false)
        } catch (e: Exception) {
            false
        }
    }

    // ----- Netplay chat UI -----

    /**
     * One chat surface: a scrolling message list over a compose
     * row (text field + Send button). The text field is a plain
     * EditText, so the Android TV system keyboard opens on it and
     * the remote's mic button gives voice typing for free.
     *
     * Messages live in the Go session (Mobile.netplayChatJSON);
     * the box polls with its own cursor, so the room box and the
     * pause box render the same conversation independently.
     */
    private inner class ChatBox {
        val root = LinearLayout(this@EmulatorActivity)
        private val scroll = ScrollView(this@EmulatorActivity)
        private val messages = TextView(this@EmulatorActivity)
        private val input = EditText(this@EmulatorActivity)
        private val shown = StringBuilder()
        private var lastSeq = 0

        init {
            root.orientation = LinearLayout.VERTICAL

            messages.textSize = 18f
            messages.setTextColor(Color.WHITE)
            messages.setPadding(18, 12, 18, 12)
            messages.text = "No messages yet. Say hello!"
            scroll.addView(messages)
            scroll.setBackgroundColor(Color.argb(90, 0, 0, 0))
            root.addView(
                scroll,
                LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f
                )
            )

            val row = LinearLayout(this@EmulatorActivity)
            row.orientation = LinearLayout.HORIZONTAL
            row.setPadding(0, 10, 0, 0)
            input.hint = "Type a message…"
            input.setSingleLine(true)
            input.isFocusable = true
            input.isFocusableInTouchMode = true
            input.textSize = 19f
            input.setTextColor(Color.WHITE)
            input.setHintTextColor(Color.argb(150, 255, 255, 255))
            input.imeOptions = EditorInfo.IME_ACTION_SEND
            input.setOnEditorActionListener { _, actionId, _ ->
                if (actionId == EditorInfo.IME_ACTION_SEND) {
                    send()
                    true
                } else {
                    false
                }
            }
            row.addView(
                input,
                LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
            )
            val sendBtn = Button(this@EmulatorActivity)
            sendBtn.text = "Send"
            sendBtn.textSize = 19f
            sendBtn.isFocusable = true
            sendBtn.setOnClickListener { send() }
            row.addView(
                sendBtn,
                LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.WRAP_CONTENT,
                    LinearLayout.LayoutParams.WRAP_CONTENT
                )
            )
            root.addView(
                row,
                LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.MATCH_PARENT,
                    LinearLayout.LayoutParams.WRAP_CONTENT
                )
            )
        }

        fun inputHasFocus(): Boolean = input.hasFocus()

        /** Pull any messages newer than our cursor and render them. */
        fun poll() {
            val raw = try {
                Mobile.netplayChatJSON(lastSeq.toLong())
            } catch (e: Exception) {
                return
            }
            try {
                val obj = org.json.JSONObject(raw)
                lastSeq = obj.optInt("seq", lastSeq)
                val arr = obj.optJSONArray("msgs") ?: return
                if (arr.length() == 0) return
                for (i in 0 until arr.length()) {
                    val m = arr.getJSONObject(i)
                    val who = if (m.optBoolean("mine")) "You" else m.optString("from")
                    shown.append(who).append(": ")
                        .append(m.optString("text")).append('\n')
                }
                messages.text = shown.toString().trimEnd()
                scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
            } catch (e: Exception) {
                Log.w(TAG, "chat poll parse failed", e)
            }
        }

        fun send() {
            val text = input.text.toString()
            if (text.isBlank()) return
            val err = try {
                Mobile.netplaySendChat(text)
            } catch (e: Exception) {
                "chat unavailable"
            }
            if (err.isEmpty()) {
                input.setText("")
                // Render our own line at once, not on the next tick.
                poll()
            } else {
                Toast.makeText(this@EmulatorActivity, err, Toast.LENGTH_SHORT).show()
            }
        }
    }

    /** Build the pause menu chat panel (once) over the game view. */
    private fun buildChatOverlay() {
        val overlay = FrameLayout(this)
        overlay.setBackgroundColor(Color.argb(235, 14, 14, 24))
        val panel = LinearLayout(this)
        panel.orientation = LinearLayout.VERTICAL
        panel.gravity = Gravity.CENTER
        panel.setPadding(64, 48, 64, 48)
        val panelParams = FrameLayout.LayoutParams(
            920, FrameLayout.LayoutParams.WRAP_CONTENT
        )
        panelParams.gravity = Gravity.CENTER

        panel.addView(roomText("Chat", 30f, true))
        val spacer = View(this)
        panel.addView(spacer, LinearLayout.LayoutParams(1, 20))
        val box = ChatBox()
        pauseChat = box
        panel.addView(
            box.root,
            LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 560)
        )
        val spacer2 = View(this)
        panel.addView(spacer2, LinearLayout.LayoutParams(1, 20))
        val closeBtn = Button(this)
        closeBtn.text = "Back to menu"
        closeBtn.textSize = 20f
        closeBtn.isFocusable = true
        closeBtn.setOnClickListener { closeChatPanel() }
        panel.addView(closeBtn, LinearLayout.LayoutParams(360, 84))

        overlay.addView(panel, panelParams)
        overlay.visibility = View.GONE
        rootFrameRef?.addView(
            overlay,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT
            )
        )
        chatOverlay = overlay
    }

    private fun showChatPanel() {
        if (chatOverlay == null) buildChatOverlay()
        chatOverlay?.visibility = View.VISIBLE
        pauseChat?.poll()
        uiHandler.removeCallbacks(chatTicker)
        uiHandler.post(chatTicker)
    }

    private fun closeChatPanel() {
        chatOverlay?.visibility = View.GONE
        uiHandler.removeCallbacks(chatTicker)
        // The game stays parked; hand the player back their menu.
        menuOpen = false
        showPauseMenu()
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

        // Watch session: hold until the room join lands (it does
        // not wait on the match itself; WatchStep just reports no
        // frame until play is underway). A join failure ends with
        // a message instead of a frozen black screen.
        if (watching) {
            while (running && !watchStarted && watchFailed == null) {
                Thread.sleep(50)
            }
            val failed = watchFailed
            if (!running) return
            if (failed != null) {
                if (isFinishing) return
                runOnUiThread {
                    AlertDialog.Builder(this)
                        .setTitle("Could not watch this game")
                        .setMessage(failed)
                        .setPositiveButton("OK") { _, _ -> finish() }
                        .setOnDismissListener { finish() }
                        .show()
                }
                return
            }
            Log.i(TAG, "watch: stepping frames")
        }

        // Online session: hold the game until the handshake finishes
        // (the guest may still be joining). Failure ends the game
        // with a message instead of a frozen black screen.
        if (netplay) {
            while (running && !npReady && npFailed == null) {
                Thread.sleep(50)
            }
            val failed = npFailed
            if (!running) return
            if (failed != null) {
                if (npCancelled || isFinishing) return
                runOnUiThread {
                    AlertDialog.Builder(this)
                        .setTitle("Online game could not start")
                        .setMessage(failed)
                        .setPositiveButton("OK") { _, _ -> finish() }
                        .setOnDismissListener { finish() }
                        .show()
                }
                return
            }
            Log.i(TAG, "netplay: stepping frames")
        }

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
            // In watch mode most ticks run no frame at all (the
            // next frame's inputs are still in flight); those ticks
            // idle and leave the last frame on screen.
            var ranFrame = true
            if (watching) {
                ranFrame = watchStepOnce()
                if (!ranFrame) Thread.sleep(10)
            } else if (netplay) {
                val err = Mobile.netplayStep(localMask.toLong())
                if (err.isNotEmpty()) {
                    Log.w(TAG, "netplay ended: $err")
                    running = false
                    runOnUiThread {
                        AlertDialog.Builder(this)
                            .setTitle("Online game ended")
                            .setMessage(err)
                            .setPositiveButton("OK") { _, _ -> finish() }
                            .setOnDismissListener { finish() }
                            .show()
                    }
                    break
                }
            } else {
                Mobile.runFrame()
            }
            if (ranFrame) {
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
            }
            // Auto-save once a minute into the player's save slot, so
            // progress survives even if nobody remembers to save. Runs
            // here on the game thread, between frames, never racing
            // the core. Never while watching: the reconstructed game
            // is not this player's to save.
            if (autoSaveEnabled && !netplay && !watching &&
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
            uploadStats(obj, statsPlayer)
        } catch (e: Exception) {
            Log.w(TAG, "stats end failed", e)
        }
    }

    /** This box's stable id for the global board (random, kept in prefs). */
    private fun deviceId(): String {
        val prefs = getSharedPreferences("itak_prefs", MODE_PRIVATE)
        var id = prefs.getString("device_id", null)
        if (id.isNullOrEmpty()) {
            id = java.util.UUID.randomUUID().toString()
            prefs.edit().putString("device_id", id).apply()
        }
        return id
    }

    /**
     * Sends the player's cumulative per-game totals to the global
     * leaderboard server. The server keeps the greater totals per
     * (device, player, game), so re-sending the whole file is safe.
     * Fire and forget: local stats stay the source of truth and a
     * dead server must never disturb the game.
     */
    private fun uploadStats(obj: org.json.JSONObject, player: String) {
        thread(name = "stats-upload") {
            try {
                val device = deviceId()
                // Board identity (display name with initials, home
                // region), written by the Flutter side whenever the
                // current player or their profile changes. Only
                // trusted when it names this exact player.
                val prefs = getSharedPreferences("itak_prefs", MODE_PRIVATE)
                val identityMatches =
                    prefs.getString("identity_name", null) == player
                val display = if (identityMatches) {
                    prefs.getString("identity_display", null)
                        ?.takeIf { it.isNotBlank() }
                } else {
                    null
                }
                val region = if (identityMatches) {
                    prefs.getString("identity_region", null)
                        ?.takeIf { it.isNotBlank() }
                } else {
                    null
                }
                val games = obj.getJSONObject("games")
                val keys = games.keys()
                var sent = 0
                while (keys.hasNext()) {
                    val rom = keys.next()
                    val g = games.getJSONObject(rom)
                    val gameName = java.io.File(rom).name
                        .substringBeforeLast('.', java.io.File(rom).name)
                    val body = org.json.JSONObject()
                        .put("deviceId", device)
                        .put("player", player)
                        .put("game", gameName)
                        .put("plays", g.optInt("plays", 0))
                        .put("seconds", g.optInt("seconds", 0))
                        .put("lastPlayed", g.optLong("last", 0))
                    if (display != null) body.put("display", display)
                    if (region != null) body.put("region", region)
                    val conn = java.net.URL("$SERVER_BASE/stats/report")
                        .openConnection() as java.net.HttpURLConnection
                    conn.requestMethod = "POST"
                    conn.connectTimeout = 5000
                    conn.readTimeout = 5000
                    conn.doOutput = true
                    conn.setRequestProperty("Content-Type", "application/json")
                    conn.outputStream.use {
                        it.write(body.toString().toByteArray(Charsets.UTF_8))
                    }
                    val code = conn.responseCode
                    conn.disconnect()
                    if (code in 200..299) sent++
                }
                Log.i(TAG, "stats uploaded: $sent games for $player")
            } catch (e: Exception) {
                Log.w(TAG, "stats upload failed", e)
            }
        }
    }

    private fun showPauseMenu() {
        if (menuOpen) return
        menuOpen = true
        paused = true
        // Tell the other player we paused, so their side waits
        // instead of declaring the connection lost.
        if (netplay) {
            try { Mobile.netplaySetPaused(true) } catch (e: Exception) { /* ignore */ }
        }
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
        // Label/action pairs, so entries can slot in (Chat, online
        // only) without renumbering a when block.
        val entries = mutableListOf<Pair<String, () -> Unit>>()
        if (watching) {
            // Spectator menu: no save state, no resume entry, no
            // exit-with-save flow. The game on screen belongs to
            // the two players; the watcher only leaves. Chat rides
            // the same pause panel the players use.
            entries.add("Chat" to {
                keepPaused = true
                window.decorView.postDelayed({ showChatPanel() }, 200)
            })
            entries.add("Picture: $filterName" to { cycleFilter() })
            entries.add("Display: $displayName" to { toggleDisplay() })
            entries.add("Leave" to {
                try { Mobile.netplayStopWatch() } catch (e: Exception) { /* already gone */ }
                finish()
            })
        } else {
        entries.add("Resume" to { }) // resume via dismiss
        if (netplay) {
            entries.add("Chat" to {
                // Keep the game parked while the chat panel is
                // open; closing it returns to this menu.
                keepPaused = true
                window.decorView.postDelayed({ showChatPanel() }, 200)
            })
        }
        entries.add("Save Game" to { doSave() })
        entries.add("Load Game" to { doLoad() })
        entries.add(cheatLabel to {
            // Keep the game parked while the cheat picker
            // is open; its own dismiss resumes play.
            keepPaused = true
            window.decorView.postDelayed({ showCheats() }, 200)
        })
        entries.add("Picture: $filterName" to { cycleFilter() })
        entries.add("Display: $displayName" to { toggleDisplay() })
        entries.add("Exit Game" to {
            // Exit asks for confirmation first; the game
            // stays parked while the prompt is open.
            keepPaused = true
            window.decorView.postDelayed({ confirmExit() }, 200)
        })
        }
        // Note: setMessage + setItems together renders an empty list on
        // this TV's dialog theme, so the player rides in the title.
        AlertDialog.Builder(this)
            .setTitle("${File(romPath).nameWithoutExtension}  ·  ${currentPlayer()}")
            .setItems(entries.map { it.first }.toTypedArray()) { _, which ->
                entries[which].second()
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
        if (netplay) {
            try { Mobile.netplaySetPaused(false) } catch (e: Exception) { /* ignore */ }
        }
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
        if (!autoSaveEnabled || netplay) return
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
        if (netplay) {
            Toast.makeText(this, "Cheats are off in an online game", Toast.LENGTH_SHORT).show()
            resumeFromMenu()
            return
        }
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
        if (netplay) {
            Toast.makeText(this, "Saving is off in an online game", Toast.LENGTH_SHORT).show()
            return
        }
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
        if (netplay) {
            Toast.makeText(this, "Loading is off in an online game", Toast.LENGTH_SHORT).show()
            return
        }
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
            if (chatOverlay?.visibility == View.VISIBLE) {
                // Chat panel is open over the paused game: BACK
                // closes it and returns to the pause menu.
                closeChatPanel()
                return true
            }
            if (netplay && !npReady) {
                if (roomChat?.inputHasFocus() == true) {
                    // Typing in the room chat: BACK steps out of
                    // the text field instead of leaving the lobby.
                    readyButton?.requestFocus()
                    return true
                }
                // Still in the waiting room: BACK leaves the lobby.
                npCancelled = true
                Mobile.netplayStop()
                finish()
                return true
            }
            if (!menuOpen) showPauseMenu()
            return true
        }
        val btn = mapButton(keyCode)
        if (btn != null) {
            if (watching) {
                // A spectator's controller never reaches the game:
                // both seats come from the players' input streams.
                return true
            }
            if (netplay) {
                // In an online game every local device plays the
                // local player; the engine feeds the mask to the
                // right slot on both boxes.
                localMask = localMask or (1 shl btn.toInt())
                return true
            }
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
            if (watching) {
                return true
            }
            if (netplay) {
                localMask = localMask and (1 shl btn.toInt()).inv()
                return true
            }
            val slot = slotFor(event)
            if (slot != null) {
                Mobile.setButton(slot.toLong(), btn, false)
            }
            return true
        }
        return super.onKeyUp(keyCode, event)
    }

    override fun onDestroy() {
        uiHandler.removeCallbacks(roomTicker)
        uiHandler.removeCallbacks(pauseTicker)
        uiHandler.removeCallbacks(chatTicker)
        uiHandler.removeCallbacks(watchTicker)
        try {
            val im = getSystemService(android.content.Context.INPUT_SERVICE)
                as android.hardware.input.InputManager
            im.unregisterInputDeviceListener(seatDeviceListener)
        } catch (e: Exception) { /* already gone */ }
        if (netplay) {
            try { Mobile.netplayStop() } catch (e: Exception) { /* already gone */ }
        }
        if (watching) {
            try { Mobile.netplayStopWatch() } catch (e: Exception) { /* already gone */ }
        }
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
