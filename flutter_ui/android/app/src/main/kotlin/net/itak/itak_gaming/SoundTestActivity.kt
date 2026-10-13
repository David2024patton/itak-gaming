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
import android.media.AudioTrack
import android.media.MediaRecorder
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.View
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
 * bar follow it, then run the mic test and watch the mic bar
 * follow your voice. If a bar moves, that path works on this box.
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

    // Speaker state. The play thread owns its AudioTrack; other
    // threads only set flags and read the envelope.
    @Volatile private var playing = false
    @Volatile private var playAbort = false
    @Volatile private var speakerEnv = 0f
    @Volatile private var playTrack: AudioTrack? = null

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
                speakerBar.tick(if (playing) speakerEnv else 0f)
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
            ScrollView.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
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
        root.addView(makeText("Press BACK to leave this screen.", 15f, dim = true))

        // Simple vertical D-pad flow: play button, then mic button.
        playButton.nextFocusDownId = micButton.id
        micButton.nextFocusUpId = playButton.id

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
                val rate = 44100
                val minBuf = AudioTrack.getMinBufferSize(
                    rate, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT
                )
                if (minBuf <= 0) throw IllegalStateException("no stereo output on this box")
                track = AudioTrack.Builder()
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
