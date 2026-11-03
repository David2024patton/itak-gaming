package net.itak.itak_gaming

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import android.graphics.RectF
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.AudioTimestamp
import android.media.AudioTrack
import android.media.MediaRecorder
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import kotlin.concurrent.thread
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.sin

/**
 * Sound test screen. Proves audio paths with moving level bars
 * instead of ears: play a generated jingle and watch the speaker
 * bar follow it, play the game sound path test (bright tones made
 * at the SNES rate and converted inside the app, the exact path
 * games take since 1.85), then run the mic test and watch the mic
 * bar follow your voice. If a bar moves, that path works on this
 * box.
 *
 * The mic half doubles as the voice chat hardware checker: any
 * microphone the box can hear (a USB mic, or a Bluetooth mic if
 * the box exposes one as an input) moves the bar.
 */
class SoundTestActivity : Activity() {

    companion object {
        private const val TAG = "iTaKSoundTest"
        private const val REQ_MIC = 4107
    }

    private val uiHandler = Handler(Looper.getMainLooper())
    private var uiBuilt = false

    private lateinit var speakerBar: LevelBar
    private lateinit var micBar: LevelBar
    private lateinit var speakerStatus: TextView
    private lateinit var micStatus: TextView
    private lateinit var peakLabel: TextView
    private lateinit var playButton: Button
    private lateinit var micButton: Button
    private lateinit var gameButton: Button
    private lateinit var gameBar: LevelBar
    private lateinit var gameStatus: TextView
    private lateinit var delayButton: Button
    private lateinit var delayStatus: TextView
    private lateinit var debugButton: Button
    private lateinit var debugStatus: TextView

    // Delay measurement state: its own short-lived track and
    // thread, independent of the speaker tests above.
    @Volatile private var measuring = false

    // Speaker state. The play thread owns its AudioTrack; other
    // threads only set flags and read the envelope.
    @Volatile private var playing = false
    @Volatile private var playAbort = false
    @Volatile private var speakerEnv = 0f
    @Volatile private var playTrack: AudioTrack? = null

    // Game path test state: shares the playing/playAbort/playTrack
    // bookkeeping with the jingle (only one speaker test runs at a
    // time), with its own bar envelope.
    @Volatile private var gamePlaying = false
    @Volatile private var gameEnv = 0f

    // Mic state. The record thread releases its AudioRecord when
    // it exits; stopMic only flags and stops.
    @Volatile private var micRunning = false
    @Volatile private var micEnv = 0f
    @Volatile private var micPeakPct = 0
    @Volatile private var recorder: AudioRecord? = null
    private var wantMicAfterPermission = false

    // Drives both bars toward their live envelopes 25 times a
    // second: fast rise so hits register, slower fall so the bar
    // settles at zero instead of snapping off.
    private val meterTicker = object : Runnable {
        override fun run() {
            if (uiBuilt) {
                speakerBar.tick(if (playing && !gamePlaying) speakerEnv else 0f)
                gameBar.tick(if (gamePlaying) gameEnv else 0f)
                micBar.tick(if (micRunning) micEnv else 0f)
                if (micRunning) peakLabel.text = "Peak ${micPeakPct}%"
            }
            uiHandler.postDelayed(this, 40)
        }
    }

    // A chunky horizontal level bar. tick() eases the shown level
    // toward the target; the fill is green, then yellow, then red
    // as it approaches full scale.
    private class LevelBar(context: android.content.Context) : View(context) {
        private var shown = 0f
        private val bounds = RectF()
        private val clip = Path()
        private val trackPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            color = Color.rgb(16, 16, 28)
        }
        private val fillPaint = Paint(Paint.ANTI_ALIAS_FLAG)

        private fun radius(): Float = 10f * resources.displayMetrics.density

        fun tick(target: Float) {
            val t = target.coerceIn(0f, 1f)
            shown = if (t >= shown) {
                shown + (t - shown) * 0.55f
            } else {
                shown * 0.86f
            }
            if (shown < 0.004f && t == 0f) shown = 0f
            invalidate()
        }

        override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
            super.onSizeChanged(w, h, oldw, oldh)
            bounds.set(0f, 0f, w.toFloat(), h.toFloat())
            clip.reset()
            clip.addRoundRect(bounds, radius(), radius(), Path.Direction.CW)
        }

        override fun onDraw(canvas: Canvas) {
            super.onDraw(canvas)
            canvas.drawRoundRect(bounds, radius(), radius(), trackPaint)
            if (shown > 0.004f) {
                fillPaint.color = when {
                    shown < 0.75f -> Color.rgb(0, 230, 118)
                    shown < 0.92f -> Color.rgb(255, 214, 0)
                    else -> Color.rgb(255, 82, 82)
                }
                canvas.save()
                canvas.clipPath(clip)
                canvas.drawRect(0f, 0f, bounds.width() * shown, bounds.height(), fillPaint)
                canvas.restore()
            }
        }
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density + 0.5f).toInt()

    private fun makeText(text: String, size: Float, bold: Boolean = false, dim: Boolean = false): TextView {
        val tv = TextView(this)
        tv.text = text
        tv.textSize = size
        tv.setTextColor(if (dim) Color.argb(185, 255, 255, 255) else Color.WHITE)
        if (bold) tv.setTypeface(null, Typeface.BOLD)
        return tv
    }

    private fun spacer(h: Int): View {
        val v = View(this)
        v.layoutParams = LinearLayout.LayoutParams(1, dp(h))
        return v
    }

    private fun makeCard(): LinearLayout {
        val card = LinearLayout(this)
        card.orientation = LinearLayout.VERTICAL
        card.setPadding(dp(28), dp(22), dp(28), dp(22))
        val bg = GradientDrawable()
        bg.setColor(Color.rgb(30, 30, 46))
        bg.cornerRadius = dp(16).toFloat()
        bg.setStroke(dp(1), Color.argb(46, 255, 255, 255))
        card.background = bg
        return card
    }

    private fun makeButton(label: String): Button {
        val b = Button(this)
        b.text = label
        b.setAllCaps(false)
        b.textSize = 20f
        b.setTextColor(Color.WHITE)
        b.isFocusable = true
        b.id = View.generateViewId()
        b.setPadding(dp(28), 0, dp(28), 0)
        fun bg(focused: Boolean): GradientDrawable {
            val d = GradientDrawable()
            d.setColor(if (focused) Color.rgb(46, 46, 70) else Color.rgb(38, 38, 56))
            d.cornerRadius = dp(12).toFloat()
            if (focused) {
                d.setStroke(dp(3), Color.WHITE)
            } else {
                d.setStroke(dp(1), Color.argb(90, 255, 255, 255))
            }
            return d
        }
        b.background = bg(false)
        b.setOnFocusChangeListener { v, hasFocus -> v.background = bg(hasFocus) }
        return b
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val scroll = ScrollView(this)
        scroll.setBackgroundColor(Color.rgb(10, 10, 20))
        scroll.isFillViewport = true
        val root = LinearLayout(this)
        root.orientation = LinearLayout.VERTICAL
        root.setPadding(dp(48), dp(36), dp(48), dp(36))
        scroll.addView(
            root,
            ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT
            )
        )

        root.addView(makeText("Sound test", 34f, bold = true))
        root.addView(spacer(8))
        root.addView(
            makeText(
                "Play a test sound and watch the speaker bar. Talk and watch " +
                    "the mic bar. If a bar moves, that path works on this box.",
                18f, dim = true
            )
        )
        root.addView(spacer(26))

        // Speaker card.
        val speakerCard = makeCard()
        speakerCard.addView(makeText("SPEAKER", 20f, bold = true))
        speakerCard.addView(spacer(14))
        playButton = makeButton("Play test sound")
        playButton.setOnClickListener { playJingle() }
        speakerCard.addView(
            playButton,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, dp(56)
            )
        )
        speakerCard.addView(spacer(16))
        speakerBar = LevelBar(this)
        speakerCard.addView(
            speakerBar,
            LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, dp(48))
        )
        speakerCard.addView(spacer(12))
        speakerStatus = makeText("No test played yet.", 17f, dim = true)
        speakerCard.addView(speakerStatus)
        root.addView(
            speakerCard,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            )
        )
        root.addView(spacer(24))

        // Game sound path card: the same conversion path SNES and
        // GBA games take, so a sick path is audible here, not only
        // in a game.
        val gameCard = makeCard()
        gameCard.addView(makeText("GAME SOUND PATH", 20f, bold = true))
        gameCard.addView(spacer(14))
        gameButton = makeButton("Play game path test")
        gameButton.setOnClickListener { playGamePath() }
        gameCard.addView(
            gameButton,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, dp(56)
            )
        )
        gameCard.addView(spacer(16))
        gameBar = LevelBar(this)
        gameCard.addView(
            gameBar,
            LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, dp(48))
        )
        gameCard.addView(spacer(12))
        gameStatus = makeText(
            "Bright tones made at the SNES sound rate, converted inside " +
                "the app the same way games are. Clean tones, no scratch " +
                "or buzz, mean the game sound path is healthy.",
            17f, dim = true
        )
        gameCard.addView(gameStatus)
        root.addView(
            gameCard,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            )
        )
        root.addView(spacer(24))

        // Mic card.
        val micCard = makeCard()
        micCard.addView(makeText("MICROPHONE", 20f, bold = true))
        micCard.addView(spacer(14))
        val micRow = LinearLayout(this)
        micRow.orientation = LinearLayout.HORIZONTAL
        micRow.gravity = Gravity.CENTER_VERTICAL
        micButton = makeButton("Start mic test")
        micButton.setOnClickListener { onMicButton() }
        micRow.addView(
            micButton,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, dp(56)
            )
        )
        peakLabel = makeText("Peak 0%", 24f, bold = true)
        val peakParams = LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT,
            LinearLayout.LayoutParams.WRAP_CONTENT
        )
        peakParams.leftMargin = dp(24)
        micRow.addView(peakLabel, peakParams)
        micCard.addView(micRow)
        micCard.addView(spacer(16))
        micBar = LevelBar(this)
        micCard.addView(
            micBar,
            LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, dp(48))
        )
        micCard.addView(spacer(12))
        micStatus = makeText("Mic test is off.", 17f, dim = true)
        micCard.addView(micStatus)
        root.addView(
            micCard,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            )
        )
        root.addView(spacer(24))

        // Debug card: measure the sound delay on this box, and
        // send the full debug report up on the spot instead of
        // waiting for the server to ask for one.
        val debugCard = makeCard()
        debugCard.addView(makeText("DEBUG", 20f, bold = true))
        debugCard.addView(spacer(14))
        delayButton = makeButton("Measure sound delay")
        delayButton.setOnClickListener { measureDelay() }
        debugCard.addView(
            delayButton,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, dp(56)
            )
        )
        debugCard.addView(spacer(12))
        delayStatus = makeText(
            "Plays a soft tone through the game sound path and " +
                "measures how far the sound trails behind, in " +
                "milliseconds.",
            17f, dim = true
        )
        debugCard.addView(delayStatus)
        debugCard.addView(spacer(16))
        debugButton = makeButton("Send debug report now")
        debugButton.setOnClickListener { sendDebugNow() }
        debugCard.addView(
            debugButton,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, dp(56)
            )
        )
        debugCard.addView(spacer(12))
        debugStatus = makeText(
            "Sends the full report, the latest game session's " +
                "sound measurements included, to the debug server.",
            17f, dim = true
        )
        debugCard.addView(debugStatus)
        root.addView(
            debugCard,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            )
        )
        root.addView(spacer(24))
        root.addView(makeText("Press BACK to leave this screen.", 15f, dim = true))

        // Simple vertical D-pad flow: play, game path, mic, debug.
        playButton.nextFocusDownId = gameButton.id
        gameButton.nextFocusUpId = playButton.id
        gameButton.nextFocusDownId = micButton.id
        micButton.nextFocusUpId = gameButton.id
        micButton.nextFocusDownId = delayButton.id
        delayButton.nextFocusUpId = micButton.id
        delayButton.nextFocusDownId = debugButton.id
        debugButton.nextFocusUpId = delayButton.id

        setContentView(scroll)
        uiBuilt = true
        playButton.post { playButton.requestFocus() }
        uiHandler.post(meterTicker)
    }

    override fun onPause() {
        super.onPause()
        abortPlayback()
        stopMic()
    }

    override fun onDestroy() {
        uiHandler.removeCallbacks(meterTicker)
        abortPlayback()
        stopMic()
        super.onDestroy()
    }

    // Send the full debug report immediately (reason "manual"),
    // through the same chute the periodic check-in uses. The
    // upload itself runs in the chute's worker; this just queues
    // it and says so on screen.
    private fun sendDebugNow() {
        DebugChute.enqueueFull(this, "manual")
        debugStatus.text =
            "Debug report queued. It sends as soon as the network " +
                "allows, with the latest game session's sound " +
                "measurements inside."
    }

    // Measure the sound delay on this box: open a track exactly
    // the way a game does (EmuAudioOutput), feed it a soft tone
    // for a few seconds, and read the track's own presentation
    // timestamps to learn how many milliseconds of sound sit
    // between the app and the speaker. The same measurement the
    // emulator now records for every game session (see
    // AudioDiag), runnable here on demand.
    private fun measureDelay() {
        if (measuring) return
        measuring = true
        delayButton.isEnabled = false
        delayStatus.text = "Measuring..."
        thread {
            var track: AudioTrack? = null
            try {
                val rate = EmuAudioOutput.deviceOutputRate(this)
                track = EmuAudioOutput.buildTrack(rate)
                val t = track
                val bufFrames = try { t.bufferSizeInFrames } catch (e: Exception) { 0 }
                t.play()
                // A soft 220 Hz tone, written in quarter-second
                // chunks for about three seconds.
                val chunkFrames = rate / 4
                val pcm = ByteArray(chunkFrames * 4)
                var frame = 0
                val samples = mutableListOf<Double>()
                val ts = AudioTimestamp()
                var written = 0L
                val started = System.nanoTime()
                while ((System.nanoTime() - started) / 1_000_000 < 3000) {
                    for (i in 0 until chunkFrames) {
                        val s = (sin(2.0 * PI * 220.0 * (frame + i) / rate) * 0.2 * 32767).toInt()
                        val o = i * 4
                        pcm[o] = (s and 0xFF).toByte()
                        pcm[o + 1] = ((s shr 8) and 0xFF).toByte()
                        pcm[o + 2] = (s and 0xFF).toByte()
                        pcm[o + 3] = ((s shr 8) and 0xFF).toByte()
                    }
                    frame += chunkFrames
                    val n = t.write(pcm, 0, pcm.size, AudioTrack.WRITE_BLOCKING)
                    if (n > 0) written += n / 4
                    try {
                        if (t.getTimestamp(ts)) {
                            val inFlight = written - ts.framePosition
                            if (inFlight >= 0) samples.add(inFlight * 1000.0 / rate)
                        }
                    } catch (e: Exception) { /* keep measuring */ }
                }
                val routed = try {
                    val dev = t.routedDevice
                    if (dev != null) dev.productName.toString() else "unknown"
                } catch (e: Exception) {
                    "unknown"
                }
                val text = if (samples.size >= 3) {
                    val sorted = samples.sorted()
                    val median = sorted[sorted.size / 2]
                    val bufMs = if (rate > 0) bufFrames * 1000.0 / rate else 0.0
                    "Sound delay on this box: about ${"%.0f".format(median)} ms.\n" +
                        "Track runs at $rate Hz, its buffer is " +
                        "${"%.0f".format(bufMs)} ms, output to $routed."
                } else {
                    "The track would not report its timing on this " +
                        "box, so the delay could not be measured here."
                }
                uiHandler.post {
                    delayStatus.text = text
                    delayButton.isEnabled = true
                    measuring = false
                }
            } catch (e: Exception) {
                uiHandler.post {
                    delayStatus.text = "The delay measurement failed on this box."
                    delayButton.isEnabled = true
                    measuring = false
                }
            } finally {
                try { track?.stop() } catch (e: Exception) { /* already stopped */ }
                try { track?.release() } catch (e: Exception) { /* already released */ }
            }
        }
    }

    // Speaker: a rising three note jingle, generated as PCM, no
    // audio assets. 440, 554.37 and 659.25 Hz, 0.60 seconds each
    // with 0.08 second gaps, stereo so both sides are exercised.

    private val noteFreqs = doubleArrayOf(440.0, 554.37, 659.25)
    private val noteFrames = 26460
    private val gapFrames = 3528

    private fun melodyFrames(): Int =
        noteFreqs.size * noteFrames + (noteFreqs.size - 1) * gapFrames

    // One mono sample of the jingle at an absolute frame index.
    // Sine plus a little second and third harmonic reads as a soft
    // triangle tone; each note has a short attack and release so
    // it does not click, and the peak sits at a comfortable 0.62
    // of full scale.
    private fun melodySample(frame: Int): Double {
        var idx = frame
        for (n in noteFreqs.indices) {
            if (idx < noteFrames) {
                val t = idx / 44100.0
                val phase = 2.0 * PI * noteFreqs[n] * t
                var s = sin(phase) + 0.25 * sin(2.0 * phase) + 0.08 * sin(3.0 * phase)
                s /= 1.33
                var env = 1.0
                val attack = 882.0
                val release = 6615.0
                if (idx < attack) env = idx / attack
                val left = noteFrames - idx
                if (left < release) {
                    val r = left / release
                    if (r < env) env = r
                }
                return 0.62 * env * s
            }
            idx -= noteFrames
            if (n < noteFreqs.size - 1) {
                if (idx < gapFrames) return 0.0
                idx -= gapFrames
            }
        }
        return 0.0
    }

    private fun playJingle() {
        if (playing) return
        playing = true
        playAbort = false
        speakerEnv = 0f
        speakerStatus.text = "Playing the test sound now."
        thread(name = "soundtest-play", isDaemon = true) {
            var outcome = "Done. If the bar moved, this box is putting out sound."
            var track: AudioTrack? = null
            try {
                // The shared builder, same as the emulator's.
                track = EmuAudioOutput.buildTrack(44100)
                if (track.state != AudioTrack.STATE_INITIALIZED) {
                    throw IllegalStateException("audio output did not start")
                }
                playTrack = track
                track.play()
                val totalFrames = melodyFrames()
                val chunkFrames = 1024
                val chunk = ShortArray(chunkFrames * 2)
                var frame = 0
                while (frame < totalFrames && !playAbort) {
                    val n = minOf(chunkFrames, totalFrames - frame)
                    var peak = 0
                    for (i in 0 until n) {
                        val v = (melodySample(frame + i) * 32767.0).toInt()
                            .coerceIn(-32767, 32767)
                        chunk[i * 2] = v.toShort()
                        chunk[i * 2 + 1] = v.toShort()
                        val a = abs(v)
                        if (a > peak) peak = a
                    }
                    val shorts = n * 2
                    var off = 0
                    while (off < shorts && !playAbort) {
                        val w = track.write(chunk, off, shorts - off)
                        if (w < 0) throw IllegalStateException("audio write failed")
                        off += w
                    }
                    // The bar follows the signal that was just
                    // handed to the output, chunk by chunk.
                    if (!playAbort) speakerEnv = peak / 32767f
                    frame += n
                }
                // Writes return once data is buffered, not played;
                // wait for the head to reach the end so the tail
                // of the jingle is heard and metered too.
                val deadline = System.currentTimeMillis() + 2500
                while (!playAbort && System.currentTimeMillis() < deadline) {
                    val head = try {
                        track.playbackHeadPosition.toLong() and 0xFFFFFFFFL
                    } catch (e: Exception) {
                        totalFrames.toLong()
                    }
                    if (head >= totalFrames.toLong()) break
                    speakerEnv *= 0.9f
                    Thread.sleep(40)
                }
                if (playAbort) outcome = "Stopped."
            } catch (e: Exception) {
                Log.w(TAG, "speaker test failed", e)
                outcome = "Speaker error: " + (e.message ?: "unknown problem")
            } finally {
                try {
                    track?.stop()
                } catch (e: Exception) {
                }
                track?.release()
                if (playTrack === track) playTrack = null
                playing = false
                speakerEnv = 0f
                val msg = outcome
                uiHandler.post {
                    if (uiBuilt) {
                        speakerStatus.text = msg
                    }
                }
            }
        }
    }

    private fun abortPlayback() {
        playAbort = true
        try {
            playTrack?.stop()
        } catch (e: Exception) {
        }
    }

    // Game sound path test: the jingle's three notes as bright
    // band-limited square tones, generated at 32,040 Hz (the SNES
    // rate the emulator runs at since 1.95) and converted to this
    // box's output rate by the same AudioResampler the emulator
    // uses, then played on a track built by the same shared
    // builder. Rich harmonics are the point: a conversion that
    // adds scratch or buzz is obvious on square tones and
    // invisible on the soft jingle above.

    private val gameNoteFrames = 19200 // 0.60 s at 32,040 Hz
    private val gameGapFrames = 2560 // 0.08 s at 32,040 Hz

    private fun gameToneFrames(): Int =
        noteFreqs.size * gameNoteFrames + (noteFreqs.size - 1) * gameGapFrames

    // One mono sample of the game path signal at an absolute frame
    // index. Odd harmonics to the 15th, Gibbs-safe peak, and the
    // same attack and release discipline as the jingle.
    private fun gameSample(frame: Int): Double {
        var idx = frame
        for (n in noteFreqs.indices) {
            if (idx < gameNoteFrames) {
                val t = idx / 32040.0
                var s = 0.0
                var k = 1
                while (k <= 15) {
                    s += sin(2.0 * PI * noteFreqs[n] * k * t) / k
                    k += 2
                }
                s /= 2.03
                var env = 1.0
                val attack = 640.0
                val release = 4800.0
                if (idx < attack) env = idx / attack
                val left = gameNoteFrames - idx
                if (left < release) {
                    val r = left / release
                    if (r < env) env = r
                }
                return 0.5 * env * s
            }
            idx -= gameNoteFrames
            if (n < noteFreqs.size - 1) {
                if (idx < gameGapFrames) return 0.0
                idx -= gameGapFrames
            }
        }
        return 0.0
    }

    private fun playGamePath() {
        if (playing) return
        playing = true
        gamePlaying = true
        playAbort = false
        gameEnv = 0f
        gameStatus.text = "Playing the game sound path now."
        thread(name = "soundtest-gamepath", isDaemon = true) {
            var outcome = "Done. Clean tones mean the game sound path is healthy on this box."
            var track: AudioTrack? = null
            try {
                val nativeRate = EmuAudioOutput.deviceOutputRate(this)
                track = EmuAudioOutput.buildTrack(nativeRate)
                if (track.state != AudioTrack.STATE_INITIALIZED) {
                    throw IllegalStateException("audio output did not start")
                }
                playTrack = track
                track.play()
                val resampler = AudioResampler(32040, nativeRate)
                val totalFrames = gameToneFrames()
                val chunkFrames = 533 // about one video frame of audio
                var frame = 0
                var writtenTotal = 0L
                while (frame < totalFrames && !playAbort) {
                    val n = minOf(chunkFrames, totalFrames - frame)
                    val pcm = ByteArray(n * 4)
                    for (i in 0 until n) {
                        val v = (gameSample(frame + i) * 32767.0).toInt()
                            .coerceIn(-32767, 32767)
                        val o = i * 4
                        pcm[o] = (v and 0xFF).toByte()
                        pcm[o + 1] = ((v shr 8) and 0xFF).toByte()
                        pcm[o + 2] = (v and 0xFF).toByte()
                        pcm[o + 3] = ((v shr 8) and 0xFF).toByte()
                    }
                    val out = resampler.convert(pcm)
                    var off = 0
                    var peak = 0
                    while (off < out.size && !playAbort) {
                        val w = track.write(out, off, out.size - off)
                        if (w < 0) throw IllegalStateException("audio write failed")
                        off += w
                    }
                    writtenTotal += out.size / 4
                    // The bar follows the converted signal, the
                    // one the box actually plays.
                    var j = 0
                    while (j + 1 < out.size) {
                        val v = (out[j + 1].toInt() shl 8) or (out[j].toInt() and 0xFF)
                        val a = abs(v)
                        if (a > peak) peak = a
                        j += 4
                    }
                    if (!playAbort && out.isNotEmpty()) gameEnv = peak / 32767f
                    frame += n
                }
                // Writes return once data is buffered, not played;
                // wait for the head to reach the end so the tail is
                // heard and metered too.
                val deadline = System.currentTimeMillis() + 2500
                while (!playAbort && System.currentTimeMillis() < deadline) {
                    val head = try {
                        track.playbackHeadPosition.toLong() and 0xFFFFFFFFL
                    } catch (e: Exception) {
                        writtenTotal
                    }
                    if (head >= writtenTotal) break
                    gameEnv *= 0.9f
                    Thread.sleep(40)
                }
                if (playAbort) outcome = "Stopped."
            } catch (e: Exception) {
                Log.w(TAG, "game path test failed", e)
                outcome = "Game path error: " + (e.message ?: "unknown problem")
            } finally {
                try {
                    track?.stop()
                } catch (e: Exception) {
                }
                track?.release()
                if (playTrack === track) playTrack = null
                playing = false
                gamePlaying = false
                gameEnv = 0f
                val msg = outcome
                uiHandler.post {
                    if (uiBuilt) {
                        gameStatus.text = msg
                    }
                }
            }
        }
    }

    // Mic: first working input config wins. Sources in order
    // VOICE_RECOGNITION, then MIC; for each source the rates are
    // 44100, then 16000; mono PCM 16 bit throughout. The input
    // device is the system default input, so a USB mic plugged
    // into the box is picked up automatically.

    private fun onMicButton() {
        if (micRunning) {
            stopMic()
            return
        }
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED
        ) {
            startMic()
        } else {
            wantMicAfterPermission = true
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_MIC)
        }
    }

    override fun onRequestPermissionsResult(
        requestCode: Int,
        permissions: Array<out String>,
        grantResults: IntArray
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode != REQ_MIC) return
        val granted = grantResults.isNotEmpty() &&
            grantResults[0] == PackageManager.PERMISSION_GRANTED
        if (granted && wantMicAfterPermission) {
            startMic()
        } else if (!granted) {
            micStatus.text =
                "No microphone found or permission denied. This box cannot hear you."
        }
        wantMicAfterPermission = false
    }

    private fun startMic() {
        if (micRunning) return
        if (recorder != null) {
            micStatus.text = "The last mic test is still stopping. Try again in a second."
            return
        }
        val sources = intArrayOf(
            MediaRecorder.AudioSource.VOICE_RECOGNITION,
            MediaRecorder.AudioSource.MIC
        )
        val rates = intArrayOf(44100, 16000)
        var found: AudioRecord? = null
        var foundBuf = 0
        outer@ for (src in sources) {
            for (rate in rates) {
                val minBuf = AudioRecord.getMinBufferSize(
                    rate, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT
                )
                if (minBuf <= 0) continue
                val bufSize = maxOf(minBuf, 4096)
                try {
                    val rec = AudioRecord.Builder()
                        .setAudioSource(src)
                        .setAudioFormat(
                            AudioFormat.Builder()
                                .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                                .setSampleRate(rate)
                                .setChannelMask(AudioFormat.CHANNEL_IN_MONO)
                                .build()
                        )
                        .setBufferSizeInBytes(bufSize)
                        .build()
                    if (rec.state != AudioRecord.STATE_INITIALIZED) {
                        rec.release()
                        continue
                    }
                    rec.startRecording()
                    if (rec.recordingState != AudioRecord.RECORDSTATE_RECORDING) {
                        try {
                            rec.stop()
                        } catch (e: Exception) {
                        }
                        rec.release()
                        continue
                    }
                    found = rec
                    foundBuf = bufSize
                    break@outer
                } catch (e: Exception) {
                    Log.w(TAG, "mic attempt failed, source $src rate $rate", e)
                }
            }
        }
        val rec = found
        if (rec == null) {
            micStatus.text = "No microphone found on this box. Plug in a USB mic and try again."
            return
        }
        recorder = rec
        micRunning = true
        micEnv = 0f
        micPeakPct = 0
        peakLabel.text = "Peak 0%"
        micButton.text = "Stop"
        micStatus.text = "Listening. Talk now and watch the bar."
        val bufSize = foundBuf
        thread(name = "soundtest-mic", isDaemon = true) {
            val buf = ShortArray(bufSize / 2)
            var sessionPeak = 0
            while (micRunning) {
                val n = try {
                    rec.read(buf, 0, buf.size)
                } catch (e: Exception) {
                    -1
                }
                if (n > 0) {
                    var peak = 0
                    for (i in 0 until n) {
                        val a = abs(buf[i].toInt())
                        if (a > peak) peak = a
                    }
                    // Peak of this buffer over full scale drives
                    // the bar; the loudest buffer since the test
                    // started drives the percent label.
                    micEnv = peak / 32768f
                    val pct = peak * 100 / 32768
                    if (pct > sessionPeak) {
                        sessionPeak = pct
                        micPeakPct = pct
                    }
                } else if (n < 0) {
                    break
                }
            }
            try {
                rec.release()
            } catch (e: Exception) {
            }
            if (recorder === rec) recorder = null
        }
    }

    private fun stopMic() {
        val wasRunning = micRunning
        micRunning = false
        val rec = recorder
        if (rec != null) {
            try {
                rec.stop()
            } catch (e: Exception) {
            }
        }
        micEnv = 0f
        if (uiBuilt && wasRunning) {
            micButton.text = "Start mic test"
            micStatus.text = "Mic test is off."
        }
    }
}
