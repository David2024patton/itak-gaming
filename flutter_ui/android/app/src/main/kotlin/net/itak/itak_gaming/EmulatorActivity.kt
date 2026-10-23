package net.itak.itak_gaming

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.ColorDrawable
import android.graphics.drawable.StateListDrawable
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
import android.widget.AdapterView
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
        const val EXTRA_NP_PASSWORD = "np_password"
        const val EXTRA_NP_TOURNAMENT = "np_tournament"
        private const val TAG = "iTaKEmu"

        // Global leaderboard server. Must match kServerBase in
        // flutter_ui/lib/main.dart.
        private const val SERVER_BASE = "https://arcade.itak.live"
    }

    private lateinit var glView: EmuGLView
    private var coreAssetName: String = "libgo_snes.so"
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
    // The lobby password from our own create/join/watch, kept so
    // a seat rotation (below) can re-enter the room through the
    // seat endpoints without asking the player again. Blank for
    // open lobbies, which is what the endpoints expect there.
    private var npPassword: String = ""
    // The tournament name for a hosted room (Online screen,
    // optional; blank = casual room, nothing is reported).
    // Guests and watchers launch without it and learn the
    // name from the host's room envelope instead (the Go
    // session holds it; see knownTournamentName).
    private var npTournament: String = ""
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
    // Seat promotion (the host named us for seat 2): runs once,
    // and while it runs the wait line is ours, not the phase's.
    private var watchSeatBusy = false
    private var watchStatusOverride: String? = null
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

    // ----- Seats and rotation (host referee, PROTOCOL.md 4.9) -----
    // The Members list under the player lines: rebuilt only when
    // the roster JSON changes. The host taps a Watching row to
    // offer that player seat 2 for the next match.
    private var membersBox: LinearLayout? = null
    private var lastRosterJson: String = ""
    // A status line that outranks the phase text for a while
    // ("Moving Fan into seat 2..."), so a room event is seen.
    private var roomNotice: String? = null
    private var roomNoticeUntil: Long = 0L
    // A role switch (demotion to watcher, promotion to guest)
    // replaces this activity with a fresh one in the new role.
    // While one runs, this instance must not tear down the
    // shared Go bridge on its way out: the new instance owns it.
    @Volatile private var roleSwitching = false
    @Volatile private var seatDanceBusy = false
    // Match end: exactly one caller (the in-game ticker, or
    // the game thread whose step the end interrupted) drives
    // the return to the room.
    private val matchEndLock = Object()
    private var matchEndBusy = false
    private var pauseDialog: AlertDialog? = null

    // ----- Got Next (winner stays, PROTOCOL.md 4.10) -----
    // The line for seat 2. The waiting room and the watch
    // screen carry the same toggle button and "Next up"
    // line, fed by the one bridge read
    // (Mobile.netplayGotNextJSON) on their existing tickers.
    private var roomGotNextButton: Button? = null
    private var roomLineLabel: TextView? = null
    private var roomGotNextMine = false
    private var lastGotNextJson: String = ""
    private var watchGotNextButton: Button? = null
    private var watchLineLabel: TextView? = null
    private var watchGotNextMine = false
    private var lastWatchGotNextJson: String = ""
    // A rotation the host planned at match end: the name
    // that takes seat 2, and when the assign fires (a breath
    // after the room settles, from updateRoom).
    private var pendingNextUp: String? = null
    private var pendingNextUpAt: Long = 0L

    // Tournaments (host reporting, PROTOCOL.md 8)
    // The host broadcasts the room's tournament name through
    // the session (Mobile.netplaySendTournamentName) and
    // reports each match's result to the server when it ends
    // with a declared loser. The send is armed, not periodic:
    // it fires when this screen starts, once more when the
    // guest first shows (a first send can race their reader),
    // and again after every match end.
    @Volatile private var tournamentSendArmed = true
    private var tournamentPeerSeen = false
    private var tournamentRow: LinearLayout? = null
    private var tournamentLine: TextView? = null
    private var lastTournamentShown: String? = null

    // ----- Netplay text chat -----
    // Two surfaces over the one session chat log in the Go
    // engine: a box built into the waiting room panel below, and
    // a full screen panel reachable from the pause menu. Each
    // keeps its own read cursor into the log.
    private var roomChat: ChatBox? = null
    private var pauseChat: ChatBox? = null
    private var chatOverlay: FrameLayout? = null

    // Game picker (hold BACK in local play): a box art grid of
    // the current system's games for a fast switch, plus the
    // BACK gesture state (tap = pause menu, double tap = exit
    // to the app home, hold = picker). David, 2026-10-15.
    private var pickerOverlay: FrameLayout? = null
    private var pickerGrid: android.widget.GridView? = null
    private var pickerOpen = false
    private val pickerArtCache = HashMap<String, android.graphics.Bitmap>()
    private var backHeld = false
    private var backConsumed = false
    private var lastBackTapAt = 0L
    private val backLongRunnable = Runnable {
        if (backHeld && !pickerOpen && !menuOpen) {
            backConsumed = true
            openGamePicker()
        }
    }
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
            // Match end (rotation): either player can end the
            // match without ending the session. Our own send
            // surfaces through the same poll, so the pause menu's
            // "End match" and "I lost" only send; this drives
            // the return to the room for everyone.
            if (npReady) {
                val endReason = try {
                    Mobile.netplayPollMatchEnd()
                } catch (e: Exception) {
                    ""
                }
                if (endReason.isNotEmpty()) handleMatchEnd(endReason)
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
            // Host duty: the Got Next line moves whenever
            // anyone in the room joins or leaves it, in the
            // room or mid match, and this ticker runs in both.
            // The rebroadcast is what makes every list in the
            // room agree; a failure leaves the dirty flag up
            // and the next tick retries.
            if (npRole == "host") {
                try {
                    if (Mobile.netplayGotNextDirty()) {
                        Mobile.netplayBroadcastGotNextQueue()
                    }
                } catch (e: Exception) { /* silent */ }
            }
            // Host duty: the room's tournament name rides out
            // on the session so the guest's room and the
            // watchers can show it. Armed sends only (screen
            // start, the guest's arrival, each match end); a
            // send that finds no session yet just stays armed
            // for the next tick.
            if (npRole == "host" && npTournament.isNotEmpty() &&
                tournamentSendArmed
            ) {
                try {
                    if (Mobile.netplaySendTournamentName(npTournament)
                            .isEmpty()
                    ) {
                        tournamentSendArmed = false
                    }
                } catch (e: Exception) { /* stays armed */ }
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

        val coreAsset = intent.getStringExtra(EXTRA_CORE) ?: "libgo_snes.so"
        coreAssetName = coreAsset
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
            // Backup-and-hold (Rust switch safety): the backend
            // needs the app files dir before any core loads, so
            // the first Rust core serve can back up saves/ and
            // hold auto-save. No-op cost on a pure-Go build: the
            // Go path never fires the trigger.
            Mobile.setFilesDir(filesDir.absolutePath)
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
        npPassword = intent.getStringExtra(EXTRA_NP_PASSWORD) ?: ""
        npTournament = intent.getStringExtra(EXTRA_NP_TOURNAMENT) ?: ""
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

        // Tournament (PROTOCOL.md 8): when the room runs under
        // a tournament name, it shows here for everyone who
        // knows it, and the host gets the Champion button that
        // crowns the event's winner on the server board. The
        // whole row hides for a casual room. refreshTournament
        // fills it in on the room ticker.
        val tRow = LinearLayout(this)
        tRow.orientation = LinearLayout.HORIZONTAL
        tRow.gravity = Gravity.CENTER
        val tLine = roomText("", 19f, true)
        tLine.setTextColor(Color.rgb(255, 213, 79))
        tRow.addView(
            tLine,
            LinearLayout.LayoutParams(
                0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f
            )
        )
        if (npRole == "host") {
            val champBtn = Button(this)
            champBtn.text = "CHAMPION"
            champBtn.textSize = 18f
            champBtn.isFocusable = true
            champBtn.setOnClickListener { pickChampion() }
            tRow.addView(
                champBtn,
                LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.WRAP_CONTENT, 76
                )
            )
        }
        tRow.visibility = View.GONE
        tournamentRow = tRow
        tournamentLine = tLine
        panel.addView(tRow)
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

        // Members: everyone in the room with their seat tag,
        // from the server roster (see refreshMembers). Bounded
        // height: a full room is two players plus ten watchers,
        // and the list scrolls instead of pushing the panel off
        // the screen. Host rows for watchers are tappable.
        panel.addView(roomText("Members", 20f, true))
        val membersScroll = ScrollView(this)
        val mb = LinearLayout(this)
        mb.orientation = LinearLayout.VERTICAL
        membersBox = mb
        membersScroll.addView(mb)
        panel.addView(
            membersScroll,
            LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 176)
        )
        val spacerM = View(this)
        panel.addView(spacerM, LinearLayout.LayoutParams(1, 24))

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

        // Got Next (winner stays, PROTOCOL.md 4.10): the line
        // for seat 2, and the button that joins or leaves it.
        // The host also gets Random challenger, which offers a
        // random watcher seat 2 through the S4 confirm flow.
        roomLineLabel = roomText("", 18f)
        roomLineLabel?.setTextColor(Color.argb(170, 255, 255, 255))
        panel.addView(roomLineLabel)
        val nextRow = LinearLayout(this)
        nextRow.orientation = LinearLayout.HORIZONTAL
        nextRow.gravity = Gravity.CENTER
        val gotNextBtn = Button(this)
        gotNextBtn.text = "GOT NEXT"
        gotNextBtn.textSize = 20f
        gotNextBtn.isFocusable = true
        gotNextBtn.setOnClickListener { toggleGotNextRoom() }
        roomGotNextButton = gotNextBtn
        nextRow.addView(gotNextBtn, LinearLayout.LayoutParams(0, 88, 1f))
        if (npRole == "host") {
            val rowGap = View(this)
            nextRow.addView(rowGap, LinearLayout.LayoutParams(20, 1))
            val randomBtn = Button(this)
            randomBtn.text = "RANDOM CHALLENGER"
            randomBtn.textSize = 20f
            randomBtn.isFocusable = true
            randomBtn.setOnClickListener { pickRandomChallenger() }
            nextRow.addView(randomBtn, LinearLayout.LayoutParams(0, 88, 1f))
        }
        panel.addView(
            nextRow,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            )
        )
        val spacerG = View(this)
        panel.addView(spacerG, LinearLayout.LayoutParams(1, 22))

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
        // Seat calls land in the room phase: the guest watches
        // for its own rotation out, everyone refreshes the
        // member list either way.
        pollSeatAssignsRoom()
        refreshMembers()
        refreshGotNextRoom()
        if (npRole == "host") firePendingNextUp()
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
        // A fresh room event (a seat move) outranks the phase
        // text until it ages out, so it is actually read.
        val notice = roomNotice
        if (notice != null && System.currentTimeMillis() < roomNoticeUntil) {
            roomStatusLine?.text = notice
        }
        refreshTournament()
        // The guest is here: if our first tournament name
        // send raced their reader, send it once more now
        // that the room has actually formed around them.
        if (npRole == "host" && !tournamentPeerSeen && peer.isNotEmpty()) {
            tournamentPeerSeen = true
            if (npTournament.isNotEmpty()) tournamentSendArmed = true
        }
        roomChat?.poll()
    }

    private fun hideRoom() {
        roomOverlay?.visibility = View.GONE
    }

    // ----- Match end: back to the room, same session -----

    /**
     * A match ended (ours or the peer's send; both surface
     * through the same one-shot poll). Exactly one caller
     * drives: the game thread whose step the end interrupted
     * and this ticker can race to the poll, so the transition
     * is claimed under a lock before anything moves.
     *
     * The session stays alive in the room phase (PROTOCOL.md
     * 4.9): no NetplayStop, no finish. The finished match is
     * recorded for stats exactly like a session end, the core
     * is reloaded to power-on (the next handshake snapshots
     * whatever the core holds; a fresh match must not inherit
     * the last one's corpse), and the next match's handshake
     * starts on its own thread behind the room UI, same as the
     * first one did.
     */
    private fun handleMatchEnd(reason: String) {
        synchronized(matchEndLock) {
            if (!npReady || matchEndBusy) return
            matchEndBusy = true
            npReady = false
        }
        runOnUiThread {
            try {
                if (isFinishing) return@runOnUiThread
                // Close whatever the match had open: the chat
                // panel, the pause menu. The dismiss listener's
                // resume is harmless here; the room owns the
                // screen now.
                if (chatOverlay?.visibility == View.VISIBLE) {
                    chatOverlay?.visibility = View.GONE
                    uiHandler.removeCallbacks(chatTicker)
                }
                pauseDialog?.let { if (it.isShowing) it.dismiss() }
                menuOpen = false
                keepPaused = false
                paused = false
                try { audioTrack?.play() } catch (e: Exception) { /* ignore */ }
                try {
                    Mobile.netplaySetPaused(false)
                } catch (e: Exception) { /* ignore */ }
                statsEnd()
                statsBegin()
                try {
                    Mobile.loadGame(romPath)
                } catch (e: Exception) {
                    Log.w(TAG, "match end reload failed", e)
                }
                localMask = 0
                npFailed = null
                Toast.makeText(this, reason, Toast.LENGTH_LONG).show()
                roomPanel?.visibility = View.VISIBLE
                roomCountLine?.visibility = View.GONE
                roomOverlay?.visibility = View.VISIBLE
                lastRosterJson = "" // force a members rebuild
                uiHandler.removeCallbacks(roomTicker)
                uiHandler.post(roomTicker)
                // Got Next: the host plans the winner-stays
                // rotation now; it fires from updateRoom once
                // the room has settled. The line itself lives
                // in the Go session and survived the match end
                // untouched, like the chat did.
                if (npRole == "host") planGotNextRotation(reason)
                if (npRole == "host") {
                    // Tournament (PROTOCOL.md 8): report the
                    // result under the room's name, and re-arm
                    // the name send so the room landing again
                    // re-states it for anyone who missed it.
                    reportTournamentResult(reason)
                    if (npTournament.isNotEmpty()) tournamentSendArmed = true
                }
                thread(name = "netplay-next") {
                    val err = try {
                        Mobile.netplayStartNextMatch()
                    } catch (e: Exception) {
                        "session error"
                    }
                    if (err.isEmpty()) {
                        npReady = true
                        Log.i(TAG, "next match ready")
                    } else {
                        if (roleSwitching || npCancelled || isFinishing) {
                            return@thread
                        }
                        npFailed = err
                        Log.w(TAG, "next match failed: $err")
                        runOnUiThread {
                            if (isFinishing) return@runOnUiThread
                            AlertDialog.Builder(this)
                                .setTitle("Online game ended")
                                .setMessage(err)
                                .setPositiveButton("OK") { _, _ -> finish() }
                                .setOnDismissListener { finish() }
                                .show()
                        }
                    }
                }
            } finally {
                synchronized(matchEndLock) { matchEndBusy = false }
            }
        }
    }

    // ----- Members list and seat control -----

    /**
     * Rebuild the Members rows from the bridge roster, but only
     * when it changed: the room ticks five times a second and a
     * rebuild under the remote's focus would drop the row the
     * host is standing on. Watchers on the host's screen are
     * tappable rows: tapping offers them seat 2.
     */
    private fun refreshMembers() {
        val box = membersBox ?: return
        val raw = try { Mobile.netplayRosterJSON() } catch (e: Exception) { return }
        if (raw == lastRosterJson) return
        lastRosterJson = raw
        box.removeAllViews()
        val arr = try {
            org.json.JSONObject(raw).optJSONArray("members")
        } catch (e: Exception) {
            null
        } ?: return
        for (i in 0 until arr.length()) {
            val m = arr.optJSONObject(i) ?: continue
            val name = m.optString("name")
            val role = m.optString("role")
            if (name.isEmpty()) continue
            val tag = when (role) {
                "host" -> "Seat 1 · Host"
                "guest" -> "Seat 2"
                else -> "Watching"
            }
            val row = roomText("$name · $tag", 20f)
            row.gravity = Gravity.CENTER_VERTICAL or Gravity.START
            if (npRole == "host" && role == "spectator") {
                row.isFocusable = true
                row.isClickable = true
                row.setPadding(18, 10, 18, 10)
                row.setOnFocusChangeListener { v, hasFocus ->
                    v.setBackgroundColor(
                        if (hasFocus) Color.argb(70, 255, 255, 255)
                        else Color.TRANSPARENT
                    )
                }
                row.setOnClickListener { confirmSeatAssign(name) }
            }
            box.addView(row)
        }
    }

    /** The host referee's one v1 call: this watcher plays next. */
    private fun confirmSeatAssign(name: String) {
        AlertDialog.Builder(this)
            .setTitle("Put $name in seat 2?")
            .setMessage("$name plays the next match in the guest seat. " +
                "Whoever holds it now moves to watching.")
            .setPositiveButton("Put in seat 2") { _, _ ->
                val err = try {
                    Mobile.netplaySendSeatAssign(name, 2)
                } catch (e: Exception) {
                    "seat change failed"
                }
                if (err.isEmpty()) {
                    roomNotice = "Moving $name into seat 2..."
                    roomNoticeUntil = System.currentTimeMillis() + 8000
                    roomStatusLine?.text = roomNotice
                } else {
                    Toast.makeText(this, err, Toast.LENGTH_LONG).show()
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    /**
     * Drain seat calls while in the room. The host never acts
     * on one (its own calls do not come back, and a claim from
     * anyone else is not the referee speaking); draining just
     * keeps the queue clean. The guest acts on exactly one
     * shape: seat 2 named for somebody else rotates the guest
     * out, and it falls back to watching.
     */
    private fun pollSeatAssignsRoom() {
        var guard = 0
        while (guard < 4) {
            val raw = try {
                Mobile.netplayPollSeatAssign()
            } catch (e: Exception) {
                return
            }
            if (raw.isEmpty()) return
            guard++
            if (npRole != "guest" || seatDanceBusy) continue
            val obj = try { org.json.JSONObject(raw) } catch (e: Exception) { continue }
            val player = obj.optString("player")
            val seat = obj.optInt("seat", 0)
            if (seat == 2 && player.isNotEmpty() && player != myName) {
                startDemotionDance()
                return
            }
        }
    }

    /**
     * Guest rotation, out: leave the player seat quietly (a
     * socket close, never a session_end: the host's session
     * would take one of those as the whole room ending), take
     * a spectator seat through the watch endpoint, and hand
     * the screen over to a fresh activity in watch mode.
     */
    private fun startDemotionDance() {
        if (seatDanceBusy || isFinishing) return
        seatDanceBusy = true
        roleSwitching = true
        npCancelled = true // the connect thread's failure is this, expected
        roomNotice = "You got rotated out. Moving to the watcher seats..."
        roomNoticeUntil = Long.MAX_VALUE
        roomStatusLine?.text = roomNotice
        thread(name = "seat-demotion") {
            try { Mobile.netplayLeaveSeat() } catch (e: Exception) { /* already gone */ }
            val token = postLobbySeat("/watch", "spectatorToken")
            runOnUiThread {
                if (isFinishing) return@runOnUiThread
                if (token == null) {
                    AlertDialog.Builder(this)
                        .setTitle("Could not move to watching")
                        .setMessage("The seat change went through, but this box " +
                            "could not take a watcher seat. The room goes on without you.")
                        .setPositiveButton("OK") { _, _ -> finish() }
                        .setOnDismissListener { finish() }
                        .show()
                    return@runOnUiThread
                }
                val intent = Intent(this, EmulatorActivity::class.java)
                intent.putExtra(EXTRA_CORE, coreAssetName)
                intent.putExtra(EXTRA_ROM, romPath)
                intent.putExtra(EXTRA_NP_MODE, "watch")
                intent.putExtra(EXTRA_NP_SERVER, npServer)
                intent.putExtra(EXTRA_NP_LOBBY, npLobby)
                intent.putExtra(EXTRA_NP_TOKEN, token)
                intent.putExtra(EXTRA_NP_PLAYER, myName)
                intent.putExtra(EXTRA_NP_PASSWORD, npPassword)
                startActivity(intent)
                finish()
            }
        }
    }

    /**
     * POST {player, password} to one of the lobby seat
     * endpoints (/watch or /join) and return the token field
     * of the reply, or null. A seat changes hands over a few
     * seconds (the outgoing player's socket has to close
     * before the server frees the name or the seat), so a
     * "not yet" answer (seat taken, name taken, rate brake)
     * retries every 2 seconds for up to 20 seconds; a wrong
     * password or a gone lobby answers at once. Blocking:
     * call from a background thread only.
     */
    private fun postLobbySeat(path: String, tokenField: String): String? {
        val base = npServer.ifEmpty { SERVER_BASE }
        val deadline = System.currentTimeMillis() + 20_000
        while (true) {
            var retryable = true
            try {
                val body = org.json.JSONObject()
                    .put("player", myName)
                    .put("password", npPassword)
                val conn = java.net.URL("$base/lobbies/$npLobby$path")
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
                if (code in 200..299) {
                    val text = conn.inputStream.bufferedReader().readText()
                    conn.disconnect()
                    val token = try {
                        org.json.JSONObject(text).optString(tokenField)
                    } catch (e: Exception) {
                        ""
                    }
                    if (token.isNotEmpty()) return token
                } else {
                    conn.disconnect()
                    // A gone lobby and a wrong password will
                    // still be gone and wrong in 2 seconds.
                    retryable = code != 401 && code != 404
                }
            } catch (e: Exception) {
                Log.w(TAG, "seat endpoint $path failed", e)
            }
            if (!retryable || System.currentTimeMillis() + 2000 > deadline) {
                return null
            }
            Thread.sleep(2000)
        }
    }

    // ----- Got Next: the line UI and the host's rotation -----

    /** Names out of a netplayGotNextJSON payload, in order. */
    private fun gotNextNames(obj: org.json.JSONObject): List<String> {
        val arr = obj.optJSONArray("queue") ?: return emptyList()
        val out = ArrayList<String>()
        for (i in 0 until arr.length()) {
            val n = arr.optString(i)
            if (n.isNotEmpty()) out.add(n)
        }
        return out
    }

    /** The line as the bridge reports it right now. */
    private fun gotNextQueueNames(): List<String> {
        val raw = try { Mobile.netplayGotNextJSON() } catch (e: Exception) {
            return emptyList()
        }
        val obj = try { org.json.JSONObject(raw) } catch (e: Exception) {
            return emptyList()
        }
        return gotNextNames(obj)
    }

    /** "Next up: A, B, C" for a line, first three names. */
    private fun nextUpText(names: List<String>): String =
        if (names.isEmpty()) {
            "Next up: the line is empty"
        } else {
            "Next up: " + names.take(3).joinToString(", ") +
                if (names.size > 3) " +${names.size - 3} more" else ""
        }

    /**
     * Refresh the room's Got Next row from the bridge, but
     * only when the payload changed (same discipline as the
     * members list: the room ticks five times a second and
     * the row under the remote must not churn).
     */
    private fun refreshGotNextRoom() {
        val raw = try { Mobile.netplayGotNextJSON() } catch (e: Exception) { return }
        if (raw == lastGotNextJson) return
        lastGotNextJson = raw
        val obj = try { org.json.JSONObject(raw) } catch (e: Exception) { return }
        roomGotNextMine = obj.optBoolean("mine", false)
        val pos = obj.optInt("position", 0)
        roomGotNextButton?.text =
            if (roomGotNextMine) "IN LINE #$pos · TAP TO LEAVE" else "GOT NEXT"
        roomLineLabel?.text = nextUpText(gotNextNames(obj))
    }

    /** The room's Got Next button: join the line, or leave it. */
    private fun toggleGotNextRoom() {
        val err = try {
            Mobile.netplaySendGotNext(!roomGotNextMine)
        } catch (e: Exception) {
            "could not update the line"
        }
        if (err.isNotEmpty()) {
            Toast.makeText(this, err, Toast.LENGTH_SHORT).show()
        }
        lastGotNextJson = "" // re-read on the next room tick
    }

    /**
     * Random challenger: the host's other referee toy. Picks
     * one of the room's watchers at random and offers them
     * seat 2 through the exact S4 confirm flow, so a random
     * pick and a tapped pick land identically. (Picking two
     * random players and sitting the host out is not in the
     * v1 seat model: seat 1 is the referee's house seat.)
     */
    private fun pickRandomChallenger() {
        val raw = try { Mobile.netplayRosterJSON() } catch (e: Exception) { return }
        val arr = try {
            org.json.JSONObject(raw).optJSONArray("members")
        } catch (e: Exception) {
            null
        } ?: return
        val watchers = ArrayList<String>()
        for (i in 0 until arr.length()) {
            val m = arr.optJSONObject(i) ?: continue
            if (m.optString("role") == "spectator") {
                val n = m.optString("name")
                if (n.isNotEmpty()) watchers.add(n)
            }
        }
        if (watchers.isEmpty()) {
            Toast.makeText(
                this, "Nobody is watching right now", Toast.LENGTH_SHORT
            ).show()
            return
        }
        confirmSeatAssign(watchers.random())
    }

    /**
     * Host rotation planning, called from the match-end path
     * as the room lands. Winner stays, v1 shape: only seat 2
     * rotates (seat 1 is the host referee's house seat), so a
     * rotation can be planned only when the guest lost and
     * someone eligible is in line. The loser is read out of
     * the match end's own words: the pause menu's "I lost"
     * sends "<name> lost", and the host's "End match" reason
     * never takes that shape, so it never plans anything.
     * The assign itself fires a breath later, from
     * updateRoom, once the line can be re-read settled.
     */
    private fun planGotNextRotation(reason: String) {
        if (!reason.endsWith(" lost")) return
        val loser = reason.removeSuffix(" lost").trim()
        if (loser.isEmpty()) return
        if (loser == myName) {
            // The host lost: the house seat rule. The host
            // keeps seat 1 and referees on; the line keeps
            // waiting for the next seat 2 opening, and the
            // room says so instead of silently skipping it.
            if (gotNextQueueNames().isNotEmpty()) {
                roomNotice = "House seat: the line plays for seat 2"
                roomNoticeUntil = System.currentTimeMillis() + 8000
                roomStatusLine?.text = roomNotice
            }
            return
        }
        val peer = try { Mobile.netplayPeerName() } catch (e: Exception) { "" }
        if (peer.isEmpty() || loser != peer) return
        val next = gotNextQueueNames()
            .firstOrNull { it != myName && it != loser } ?: return
        pendingNextUp = next
        pendingNextUpAt = System.currentTimeMillis() + 2000
    }

    /**
     * Fire a planned rotation once its moment comes: the
     * planned name takes the loser's seat through the same
     * seat_assign a manual pick sends, and leaves the line
     * as they take it (NoteSeatTaken). If the line moved on
     * without them in the meantime, the plan dies quietly.
     */
    private fun firePendingNextUp() {
        val next = pendingNextUp ?: return
        if (System.currentTimeMillis() < pendingNextUpAt) return
        pendingNextUp = null
        if (!gotNextQueueNames().contains(next)) return
        val err = try {
            Mobile.netplaySendSeatAssign(next, 2)
        } catch (e: Exception) {
            "seat change failed"
        }
        if (err.isEmpty()) {
            try { Mobile.netplayNoteSeatTaken(next) } catch (e: Exception) { /* ignore */ }
            roomNotice = "Winner stays. Next up: $next"
            roomNoticeUntil = System.currentTimeMillis() + 8000
            roomStatusLine?.text = roomNotice
        }
    }

    // Tournaments: name display, reporting, champion

    /**
     * The tournament name as this client knows it: the host's
     * own (from its launch) first, the session's copy for
     * everyone else (the host's envelope settled it there).
     */
    private fun knownTournamentName(): String {
        if (npTournament.isNotEmpty()) return npTournament
        return try {
            Mobile.netplayTournamentName()
        } catch (e: Exception) {
            ""
        }
    }

    /**
     * Show or hide the room's tournament row from the name as
     * currently known. Changed-only, like the members list:
     * the room ticks five times a second and the row must not
     * churn under the remote.
     */
    private fun refreshTournament() {
        val name = knownTournamentName()
        if (name == lastTournamentShown) return
        lastTournamentShown = name
        tournamentLine?.text =
            if (name.isEmpty()) "" else "Tournament: $name"
        tournamentRow?.visibility =
            if (name.isEmpty()) View.GONE else View.VISIBLE
    }

    /**
     * This box's board identity (display name, home region)
     * for a player, from the prefs the Flutter side keeps,
     * trusted only when they name this exact player (the same
     * rule uploadStats uses). Only the host's own identity is
     * ever known here; a report carries it for whichever seat
     * the host holds and leaves the other seat's fields out,
     * and the server fills those in from that player's own
     * reports.
     */
    private fun identityFor(player: String): Pair<String?, String?> {
        val prefs = getSharedPreferences("itak_prefs", MODE_PRIVATE)
        if (prefs.getString("identity_name", null) != player) {
            return null to null
        }
        val display = prefs.getString("identity_display", null)
            ?.takeIf { it.isNotBlank() }
        val region = prefs.getString("identity_region", null)
            ?.takeIf { it.isNotBlank() }
        return display to region
    }

    /**
     * POST a JSON body to the game server, fire and forget.
     * Background thread only. A bad answer or a dead server
     * is logged and nothing more: tournament bookkeeping
     * must never disturb the room it is bookkeeping for.
     */
    private fun postToServer(path: String, body: org.json.JSONObject) {
        try {
            val base = npServer.ifEmpty { SERVER_BASE }
            val conn = java.net.URL("$base$path")
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
            if (code !in 200..299) {
                Log.w(TAG, "POST $path answered $code")
            }
        } catch (e: Exception) {
            Log.w(TAG, "POST $path failed", e)
        }
    }

    /**
     * Host reporting, called from the match-end path when the
     * room runs under a tournament name. The result counts
     * only when the match ended with a declared loser, read
     * out of the reason the way the Got Next rotation reads
     * it: the pause menu's "I lost" sends "<name> lost", and
     * the host's "End match" never takes that shape, so it
     * never reports. With no loser there is nothing to send,
     * and the room says so instead of silently skipping it.
     */
    private fun reportTournamentResult(reason: String) {
        val tName = npTournament.trim()
        if (tName.isEmpty()) return
        if (!reason.endsWith(" lost")) {
            roomNotice = "Not counted: no loser declared"
            roomNoticeUntil = System.currentTimeMillis() + 8000
            roomStatusLine?.text = roomNotice
            return
        }
        val loser = reason.removeSuffix(" lost").trim()
        val peer = try {
            Mobile.netplayPeerName()
        } catch (e: Exception) {
            ""
        }
        val winner = when (loser) {
            myName -> peer
            peer -> myName
            else -> ""
        }
        if (loser.isEmpty() || winner.isEmpty()) return
        val body = org.json.JSONObject()
            .put("tournament", tName)
            .put("game", java.io.File(romPath).name)
            .put("winner", winner)
            .put("loser", loser)
            .put("host", myName)
        val (wDisplay, wRegion) = identityFor(winner)
        if (wDisplay != null) body.put("winnerDisplay", wDisplay)
        if (wRegion != null) body.put("winnerRegion", wRegion)
        val (lDisplay, lRegion) = identityFor(loser)
        if (lDisplay != null) body.put("loserDisplay", lDisplay)
        if (lRegion != null) body.put("loserRegion", lRegion)
        thread(name = "tournament-report") {
            postToServer("/matches/report", body)
        }
    }

    /**
     * The Champion button: pick one of the room's current
     * members to crown as the tournament's winner. The
     * roster is the same list the Members rows show, seats
     * and watchers alike: a tournament's winner is the
     * host's call, and the board is honor system anyway.
     */
    private fun pickChampion() {
        val tName = knownTournamentName()
        if (tName.isEmpty()) return
        val raw = try {
            Mobile.netplayRosterJSON()
        } catch (e: Exception) {
            return
        }
        val arr = try {
            org.json.JSONObject(raw).optJSONArray("members")
        } catch (e: Exception) {
            null
        } ?: return
        val names = ArrayList<String>()
        for (i in 0 until arr.length()) {
            val n = arr.optJSONObject(i)?.optString("name") ?: ""
            if (n.isNotEmpty()) names.add(n)
        }
        if (names.isEmpty()) {
            Toast.makeText(
                this, "Nobody is in the room yet", Toast.LENGTH_SHORT
            ).show()
            return
        }
        AlertDialog.Builder(this)
            .setTitle("Tournament champion")
            .setItems(arrayOf<CharSequence>(*names.toTypedArray())) { _, which ->
                confirmChampion(tName, names[which])
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    /**
     * Confirm and post the champion, in confirmSeatAssign's
     * shape: a crown lands on a public board, so it gets the
     * same second thought a seat change gets.
     */
    private fun confirmChampion(tournament: String, champion: String) {
        AlertDialog.Builder(this)
            .setTitle("Crown $champion?")
            .setMessage("$champion is recorded as the winner of " +
                "$tournament on the tournament board.")
            .setPositiveButton("Champion") { _, _ ->
                val body = org.json.JSONObject()
                    .put("tournament", tournament)
                    .put("champion", champion)
                    .put("host", myName)
                val (display, region) = identityFor(champion)
                if (display != null) body.put("championDisplay", display)
                if (region != null) body.put("championRegion", region)
                thread(name = "tournament-champion") {
                    postToServer("/matches/champion", body)
                }
                roomNotice = "Champion: $champion"
                roomNoticeUntil = System.currentTimeMillis() + 8000
                roomStatusLine?.text = roomNotice
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    /**
     * Refresh the watch screen's Got Next bar from the
     * bridge, same changed-only discipline as the room's.
     */
    private fun refreshWatchGotNext() {
        val raw = try { Mobile.netplayGotNextJSON() } catch (e: Exception) { return }
        if (raw == lastWatchGotNextJson) return
        lastWatchGotNextJson = raw
        val obj = try { org.json.JSONObject(raw) } catch (e: Exception) { return }
        watchGotNextMine = obj.optBoolean("mine", false)
        val pos = obj.optInt("position", 0)
        watchGotNextButton?.text =
            if (watchGotNextMine) "IN LINE #$pos · TAP TO LEAVE" else "GOT NEXT"
        watchLineLabel?.text = nextUpText(gotNextNames(obj))
    }

    /** The watch screen's Got Next button: join, or leave. */
    private fun toggleGotNextWatch() {
        val err = try {
            Mobile.netplaySendGotNext(!watchGotNextMine)
        } catch (e: Exception) {
            "could not update the line"
        }
        if (err.isNotEmpty()) {
            Toast.makeText(this, err, Toast.LENGTH_SHORT).show()
        }
        lastWatchGotNextJson = "" // re-read on the next watch tick
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

        // Got Next (PROTOCOL.md 4.10): the line for seat 2
        // lives at the bottom of the watch screen. Watchers
        // are who the line is mostly for: tap to get in, win
        // or rotate in through the S4 promotion when the call
        // comes.
        val bar = LinearLayout(this)
        bar.orientation = LinearLayout.VERTICAL
        bar.gravity = Gravity.CENTER
        val line = TextView(this)
        line.textSize = 18f
        line.setTextColor(Color.WHITE)
        line.setBackgroundColor(Color.argb(160, 20, 20, 32))
        line.setPadding(24, 10, 24, 10)
        line.gravity = Gravity.CENTER
        watchLineLabel = line
        bar.addView(line)
        val nextBtn = Button(this)
        nextBtn.text = "GOT NEXT"
        nextBtn.textSize = 20f
        nextBtn.isFocusable = true
        nextBtn.setOnClickListener { toggleGotNextWatch() }
        watchGotNextButton = nextBtn
        val nextBtnParams = LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, 84
        )
        nextBtnParams.topMargin = 12
        bar.addView(nextBtn, nextBtnParams)
        val barParams = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT
        )
        barParams.gravity = Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL
        barParams.bottomMargin = 28
        root.addView(bar, barParams)
    }

    /**
     * Drain seat calls while watching. The host referee naming
     * us for seat 2 is a promotion: leave the watcher seat,
     * take the guest seat through the join endpoint, and hand
     * the screen to a fresh activity as a player. A call naming
     * anyone else needs nothing from us; the roster and the
     * banner already follow the room.
     */
    private fun pollWatchSeatAssigns() {
        if (watchSeatBusy) return
        var guard = 0
        while (guard < 4) {
            val raw = try {
                Mobile.netplayPollSeatAssign()
            } catch (e: Exception) {
                return
            }
            if (raw.isEmpty()) return
            guard++
            val obj = try { org.json.JSONObject(raw) } catch (e: Exception) { continue }
            if (obj.optString("player") == myName && obj.optInt("seat", 0) == 2) {
                startPromotionDance()
                return
            }
        }
    }

    /** Watcher rotation, in: swap the spectator token for a seat. */
    private fun startPromotionDance() {
        if (watchSeatBusy || isFinishing) return
        watchSeatBusy = true
        roleSwitching = true
        watchStatusOverride = "Taking your seat..."
        watchWaitLine?.visibility = View.VISIBLE
        watchWaitLine?.text = watchStatusOverride
        thread(name = "seat-promotion") {
            try { Mobile.netplayStopWatch() } catch (e: Exception) { /* already gone */ }
            val token = postLobbySeat("/join", "guestToken")
            runOnUiThread {
                if (isFinishing) return@runOnUiThread
                if (token == null) {
                    AlertDialog.Builder(this)
                        .setTitle("Could not take the seat")
                        .setMessage("The host put you in seat 2, but this box " +
                            "could not join as the guest in time.")
                        .setPositiveButton("OK") { _, _ -> finish() }
                        .setOnDismissListener { finish() }
                        .show()
                    return@runOnUiThread
                }
                val intent = Intent(this, EmulatorActivity::class.java)
                intent.putExtra(EXTRA_CORE, coreAssetName)
                intent.putExtra(EXTRA_ROM, romPath)
                intent.putExtra(EXTRA_NP_ROLE, "guest")
                intent.putExtra(EXTRA_NP_LOBBY, npLobby)
                intent.putExtra(EXTRA_NP_TOKEN, token)
                intent.putExtra(EXTRA_NP_SERVER, npServer)
                intent.putExtra(EXTRA_NP_PASSWORD, npPassword)
                startActivity(intent)
                finish()
            }
        }
    }

    /** Poll the watch state: banner names, waiting line, the end. */
    private fun updateWatchBanner() {
        pollWatchSeatAssigns()
        refreshWatchGotNext()
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
            // The room's tournament name rides along while
            // the match forms, when the watcher knows it: the
            // host's envelope settled it in the watch session.
            val tName = try {
                Mobile.netplayTournamentName()
            } catch (e: Exception) {
                ""
            }
            val versus = if (host.isNotEmpty() && guest.isNotEmpty()) {
                "\n$host vs $guest"
            } else {
                ""
            }
            val event = if (tName.isNotEmpty()) {
                "\nTournament: $tName"
            } else {
                ""
            }
            watchWaitLine?.text = "Waiting for the match to start$versus$event"
        }
        // A seat promotion in flight owns the wait line (and is
        // not a watch ending, whatever the phase says now that
        // the watch session is stopped).
        val override = watchStatusOverride
        if (override != null) {
            watchWaitLine?.visibility = View.VISIBLE
            watchWaitLine?.text = override
            return
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

    // ---------- Game picker (hold BACK) ----------

    // Double tap BACK: leave the game for the app home, with
    // the same auto-save the confirmed exit makes.
    private fun exitGameDirect() {
        autoSaveNow()
        finish()
    }

    private val pickerExts = mapOf(
        "NES" to listOf(".nes"),
        "SNES" to listOf(".sfc", ".smc"),
        "Genesis" to listOf(".md", ".gen", ".bin"),
        "SegaCD" to listOf(".cue", ".iso", ".bin"),
        "GameBoy" to listOf(".gb", ".gbc"),
        "GBA" to listOf(".gba"),
        "PS1" to listOf(".cue", ".iso", ".bin"),
        "N64" to listOf(".n64", ".z64", ".v64"),
        "Dreamcast" to listOf(".gdi", ".iso", ".bin"),
        "PSP" to listOf(".iso", ".elf", ".prx"),
        "NDS" to listOf(".nds"),
        "Saturn" to listOf(".cue", ".iso"),
        "GameCube" to listOf(".iso", ".gcm"),
    )

    private fun pickerGames(): List<File> {
        val dir = File(romPath).parentFile ?: return emptyList()
        val exts = pickerExts[dir.name] ?: return emptyList()
        val files = dir.listFiles() ?: return emptyList()
        return files.filter {
            it.isFile && exts.any { e -> it.name.lowercase().endsWith(e) }
        }.sortedBy { it.name.lowercase() }
    }

    private fun openGamePicker() {
        if (pickerOpen) return
        val games = pickerGames()
        if (games.isEmpty()) {
            Toast.makeText(this, "No games found in this folder", Toast.LENGTH_SHORT).show()
            return
        }
        // Park the game exactly like the pause menu does.
        pickerOpen = true
        paused = true
        var waited = 0
        while (!pauseAck && waited < 200) {
            Thread.sleep(5)
            waited += 5
        }
        try { audioTrack?.pause() } catch (e: Exception) { /* ignore */ }
        if (pickerOverlay == null) buildGamePicker(games)
        pickerOverlay?.visibility = View.VISIBLE
        pickerGrid?.requestFocus()
        pickerGrid?.setSelection(
            games.indexOfFirst { it.path == romPath }.coerceAtLeast(0)
        )
    }

    private fun closeGamePicker() {
        pickerOverlay?.visibility = View.GONE
        pickerOpen = false
        paused = false
        try { audioTrack?.play() } catch (e: Exception) { /* ignore */ }
    }

    private fun buildGamePicker(games: List<File>) {
        val overlay = FrameLayout(this)
        overlay.setBackgroundColor(Color.argb(242, 10, 10, 20))
        val panel = LinearLayout(this)
        panel.orientation = LinearLayout.VERTICAL
        panel.setPadding(56, 40, 56, 40)
        val dirName = File(romPath).parentFile?.name ?: ""
        panel.addView(roomText("Switch game  -  $dirName", 30f, true))
        panel.addView(
            roomText(
                "Pick a game to jump straight into it. BACK returns to your game.",
                17f, false
            )
        )
        val grid = android.widget.GridView(this)
        grid.numColumns = 4
        grid.horizontalSpacing = 24
        grid.verticalSpacing = 24
        grid.setPadding(0, 24, 0, 0)
        grid.isFocusable = true
        grid.setDrawSelectorOnTop(true)
        val sel = android.graphics.drawable.GradientDrawable()
        sel.setStroke(6, Color.WHITE)
        sel.cornerRadius = 18f
        sel.setColor(Color.argb(40, 255, 255, 255))
        grid.selector = sel
        grid.adapter = PickerAdapter(games)
        grid.setOnItemClickListener { _, _, pos, _ -> switchToGame(games[pos]) }
        pickerGrid = grid
        panel.addView(
            grid,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f
            )
        )
        overlay.addView(
            panel,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT
            )
        )
        overlay.visibility = View.GONE
        rootFrameRef?.addView(
            overlay,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT
            )
        )
        pickerOverlay = overlay
    }

    private inner class PickerAdapter(val games: List<File>) :
        android.widget.BaseAdapter() {
        override fun getCount() = games.size
        override fun getItem(pos: Int) = games[pos]
        override fun getItemId(pos: Int) = pos.toLong()

        override fun getView(
            pos: Int,
            convert: View?,
            parent: android.view.ViewGroup
        ): View {
            val f = games[pos]
            val cell: FrameLayout
            val img: android.widget.ImageView
            val label: android.widget.TextView
            if (convert == null) {
                cell = FrameLayout(this@EmulatorActivity)
                val bg = android.graphics.drawable.GradientDrawable()
                bg.cornerRadius = 14f
                bg.setColor(0xFF22223A.toInt())
                cell.background = bg
                img = android.widget.ImageView(this@EmulatorActivity)
                img.scaleType = android.widget.ImageView.ScaleType.CENTER_CROP
                img.tag = "art"
                cell.addView(
                    img,
                    FrameLayout.LayoutParams(
                        FrameLayout.LayoutParams.MATCH_PARENT,
                        FrameLayout.LayoutParams.MATCH_PARENT
                    )
                )
                label = android.widget.TextView(this@EmulatorActivity)
                label.setTextColor(Color.WHITE)
                label.textSize = 15f
                label.maxLines = 1
                label.ellipsize = android.text.TextUtils.TruncateAt.END
                label.setPadding(10, 6, 10, 6)
                label.setBackgroundColor(Color.argb(190, 0, 0, 0))
                label.tag = "label"
                val lp = FrameLayout.LayoutParams(
                    FrameLayout.LayoutParams.MATCH_PARENT,
                    FrameLayout.LayoutParams.WRAP_CONTENT
                )
                lp.gravity = android.view.Gravity.BOTTOM
                cell.addView(label, lp)
                cell.layoutParams = android.widget.AbsListView.LayoutParams(
                    android.widget.AbsListView.LayoutParams.MATCH_PARENT, 300
                )
            } else {
                cell = convert as FrameLayout
                img = cell.findViewWithTag("art")
                label = cell.findViewWithTag("label")
            }
            val name = f.nameWithoutExtension
            label.text = if (f.path == romPath) "NOW: $name" else name
            val bmp = pickerArtFor(f)
            if (bmp != null) {
                img.setImageBitmap(bmp)
            } else {
                img.setImageDrawable(null)
            }
            return cell
        }
    }

    private fun pickerArtFor(game: File): android.graphics.Bitmap? {
        pickerArtCache[game.path]?.let { return it }
        val art = File(game.parentFile, "boxart/${game.nameWithoutExtension}.png")
        if (!art.exists()) return null
        val opts = android.graphics.BitmapFactory.Options()
        opts.inSampleSize = 2
        val bmp = try {
            android.graphics.BitmapFactory.decodeFile(art.path, opts)
        } catch (e: Exception) {
            null
        }
        if (bmp != null) pickerArtCache[game.path] = bmp
        return bmp
    }

    private fun switchToGame(target: File) {
        if (target.path == romPath) {
            closeGamePicker()
            return
        }
        // The same handoff as a role switch: the fresh
        // activity instance owns the shared bridge next, so
        // this instance must not tear it down on the way out.
        autoSaveNow()
        roleSwitching = true
        val intent = android.content.Intent(this, EmulatorActivity::class.java)
        intent.putExtra(EXTRA_CORE, coreAssetName)
        intent.putExtra(EXTRA_ROM, target.path)
        startActivity(intent)
        finish()
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

    private fun sha256Hex(stream: java.io.InputStream): String {
        val digest = java.security.MessageDigest.getInstance("SHA-256")
        val buf = ByteArray(64 * 1024)
        stream.use { input ->
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                digest.update(buf, 0, n)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    /**
     * Returns the on-disk copy of a bundled core, refreshing it
     * whenever the bundled bytes change. The old guard copied only
     * when the file was missing or empty, so the first extraction
     * (1.66-era cores) survived every app update in filesDir and
     * silently stranded the newer cores inside the APK (proven on
     * hardware 2026-10-23: the stick extracted a 401,640-byte GBA
     * core while its own 1.72 APK carried the 405,616-byte fix).
     * Now the bundled asset and the extracted file are SHA-256
     * compared on every launch; on a mismatch the asset is written
     * to a temp file in the same directory, hash-verified, then
     * renamed over the old copy (atomic same-directory rename), so
     * a device can never again run a core that does not match the
     * APK it shipped in. A refresh failure keeps the previously
     * extracted copy rather than crashing the launch.
     */
    private fun extractCore(assetName: String): File {
        val out = File(filesDir, assetName)
        try {
            val assetHash = sha256Hex(assets.open("cores/$assetName"))
            val currentHash = if (out.exists() && out.length() > 0L) {
                sha256Hex(out.inputStream())
            } else {
                null
            }
            if (currentHash == assetHash) return out
            val oldSize = if (out.exists()) out.length() else 0L
            val tmp = File(filesDir, "$assetName.refresh")
            assets.open("cores/$assetName").use { input ->
                tmp.outputStream().use { output -> input.copyTo(output) }
            }
            if (sha256Hex(tmp.inputStream()) != assetHash) {
                tmp.delete()
                throw java.io.IOException("extracted core hash mismatch for $assetName")
            }
            if (!tmp.renameTo(out)) {
                tmp.delete()
                throw java.io.IOException("could not replace extracted core $assetName")
            }
            Log.i(TAG, "core refreshed $assetName oldSize=$oldSize newSize=${out.length()}")
        } catch (e: Exception) {
            // Never let a refresh failure kill the launch: the
            // previously extracted copy (if any) still loads, and
            // a missing copy surfaces inside the guarded loadCore
            // try/catch in onCreate exactly as it did before.
            Log.e(TAG, "core refresh failed for $assetName, keeping existing copy", e)
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
                if (!npReady) {
                    // Room phase: before the first match, and
                    // between matches after a match end. The
                    // room UI owns the screen and the handshake
                    // runs on its own thread; stepping a session
                    // that is sitting in the room is an error by
                    // design, so idle instead.
                    ranFrame = false
                    Thread.sleep(30)
                } else {
                    val err = Mobile.netplayStep(localMask.toLong())
                    if (err.isNotEmpty()) {
                        // A match end lands here as a step error:
                        // the session fell back to the room
                        // mid-step (PROTOCOL.md 4.9). If a match
                        // end is on record it is a rotation, not
                        // a death; and if the ticker already took
                        // the record, npReady is down and the step
                        // stopped for the room, also not a death.
                        val endReason = try {
                            Mobile.netplayPollMatchEnd()
                        } catch (e: Exception) {
                            ""
                        }
                        if (endReason.isNotEmpty()) {
                            handleMatchEnd(endReason)
                        } else if (!npReady) {
                            Log.i(TAG, "netplay step stopped for the room: $err")
                        } else {
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
                    }
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
                    // AutoSaveState, not saveState: during the
                    // backup-and-hold hold (first session on a
                    // Rust core) it returns nothing, so this
                    // write cannot overwrite a pre-switch save.
                    val data = Mobile.autoSaveState()
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
        // Exit sits at the top on purpose (David, 2026-10-15):
        // the menu still opens on Resume, and one UP from
        // there is the quick way out.
        entries.add("Exit Game" to {
            // Exit asks for confirmation first; the game
            // stays parked while the prompt is open.
            keepPaused = true
            window.decorView.postDelayed({ confirmExit() }, 200)
        })
        entries.add("Resume" to { }) // resume via dismiss
        if (netplay) {
            entries.add("Chat" to {
                // Keep the game parked while the chat panel is
                // open; closing it returns to this menu.
                keepPaused = true
                window.decorView.postDelayed({ showChatPanel() }, 200)
            })
            // Match end (rotation): these only send. The event
            // lands back through PollMatchEnd, ours included,
            // and the ticker drives the return to the room, so
            // both players arrive there together.
            if (npRole == "host") {
                entries.add("End match" to {
                    val err = try {
                        Mobile.netplaySendMatchEnd("Host ended the match")
                    } catch (e: Exception) {
                        "could not end the match"
                    }
                    if (err.isNotEmpty()) {
                        Toast.makeText(this, err, Toast.LENGTH_SHORT).show()
                    }
                })
            }
            entries.add("I lost" to {
                val err = try {
                    Mobile.netplaySendMatchEnd("$myName lost")
                } catch (e: Exception) {
                    "could not end the match"
                }
                if (err.isNotEmpty()) {
                    Toast.makeText(this, err, Toast.LENGTH_SHORT).show()
                }
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
        // Exit Game appears at both ends of the menu on
        // purpose (David, 2026-10-15): top for a fast exit
        // from Resume, bottom where it has always been.
        entries.add("Exit Game" to {
            keepPaused = true
            window.decorView.postDelayed({ confirmExit() }, 200)
        })
        }
        // Note: setMessage + setItems together renders an empty list on
        // this TV's dialog theme, so the player rides in the title.
        // The dialog is kept in a field so a match end landing
        // while the menu is open can close it (handleMatchEnd).
        pauseDialog = AlertDialog.Builder(this)
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
        // Make the current row impossible to miss (David,
        // 2026-10-15): the TV dialog theme's own selector is a
        // faint bar nobody can see, his parents included. The
        // selected row gets a solid white bar with black bold
        // text, the same language as the update dialog buttons.
        pauseDialog?.listView?.let { lv ->
            val sel = StateListDrawable()
            val bar = ColorDrawable(Color.WHITE)
            sel.addState(intArrayOf(android.R.attr.state_focused), bar)
            sel.addState(intArrayOf(android.R.attr.state_selected), bar)
            sel.addState(intArrayOf(android.R.attr.state_pressed), bar)
            sel.addState(intArrayOf(), ColorDrawable(Color.TRANSPARENT))
            lv.selector = sel
            val paint = {
                val chosen = lv.selectedItemPosition
                for (i in 0 until lv.childCount) {
                    val row = lv.getChildAt(i) as? TextView ?: continue
                    if (lv.firstVisiblePosition + i == chosen) {
                        row.setTextColor(Color.BLACK)
                        row.setTypeface(row.typeface, Typeface.BOLD)
                    } else {
                        row.setTextColor(Color.WHITE)
                        row.setTypeface(row.typeface, Typeface.NORMAL)
                    }
                }
            }
            lv.onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
                override fun onItemSelected(
                    parent: AdapterView<*>?, view: View?, pos: Int, id: Long
                ) {
                    lv.post { paint() }
                }

                override fun onNothingSelected(parent: AdapterView<*>?) { }
            }
            lv.post {
                // The player menu opens on Resume (index 1; Exit
                // Game is index 0 now), never on Exit itself.
                if (!watching) lv.setSelection(1)
                paint()
            }
        }
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
                if (Mobile.backupHoldActive())
                    "Auto-save is paused for this first session on the new core, to protect your older saves. Anything you have not saved will be lost."
                else if (autoSaveEnabled)
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
            // An exit save is automatic, so it honors the
            // backup-and-hold hold like the timer save does.
            val data = Mobile.autoSaveState()
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
        // A manual save is one of the hold's safe ends: the
        // slot now holds a state this core wrote on purpose.
        Mobile.noteManualSave()
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
            if (netplay || watching) {
                if (!menuOpen) showPauseMenu()
                return true
            }
            // Local play: BACK is a small gesture language.
            // A tap opens the pause menu, a double tap exits
            // to the app home, and holding opens the game
            // picker. The tap resolves on key up so the three
            // can be told apart.
            if (pickerOpen) {
                backConsumed = true
                closeGamePicker()
                return true
            }
            if (menuOpen) return true
            if (event.repeatCount > 0) return true
            backHeld = true
            backConsumed = false
            uiHandler.removeCallbacks(backLongRunnable)
            uiHandler.postDelayed(backLongRunnable, 550)
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
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            if (!netplay && !watching) {
                uiHandler.removeCallbacks(backLongRunnable)
                backHeld = false
                if (pickerOpen || menuOpen) return true
                if (backConsumed) {
                    backConsumed = false
                    return true
                }
                // A plain tap, or the second tap of a pair.
                val now = System.currentTimeMillis()
                if (now - lastBackTapAt < 400) {
                    lastBackTapAt = 0L
                    exitGameDirect()
                } else {
                    lastBackTapAt = now
                    showPauseMenu()
                }
            }
            return true
        }
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
        uiHandler.removeCallbacks(backLongRunnable)
        try {
            val im = getSystemService(android.content.Context.INPUT_SERVICE)
                as android.hardware.input.InputManager
            im.unregisterInputDeviceListener(seatDeviceListener)
        } catch (e: Exception) { /* already gone */ }
        // A role switch hands this screen to a fresh activity
        // in the new role, and that instance already owns the
        // shared bridge (it reloaded the core and started its
        // own session). Tearing the bridge down here would
        // close the new instance's game out from under it.
        if (!roleSwitching) {
            if (netplay) {
                try { Mobile.netplayStop() } catch (e: Exception) { /* already gone */ }
            }
            if (watching) {
                try { Mobile.netplayStopWatch() } catch (e: Exception) { /* already gone */ }
            }
        }
        statsEnd()
        running = false
        gameThread?.join(1000)
        audioTrack?.stop()
        audioTrack?.release()
        audioTrack = null
        if (!roleSwitching) {
            try { Mobile.close() } catch (e: Exception) { Log.w(TAG, "close failed", e) }
        }
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
