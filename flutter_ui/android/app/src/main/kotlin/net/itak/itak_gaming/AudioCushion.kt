package net.itak.itak_gaming

import android.util.Log
import kotlin.math.floor

/**
 * The audio cushion: keeps the AudioTrack fed through heavy screens.
 *
 * The problem it solves. On a heavy screen (the SNES Zelda title and
 * file select are the measured case) a frame takes longer to emulate
 * than the frame budget, so the core produces samples slower than
 * real time for seconds at a stretch (about 95.5 percent on the
 * file-select stretch). The samples themselves are clean; the stream
 * simply arrives late, the track drains its small bank, and the DAC
 * starves in bursts. That starvation is the tearing/warbling sound.
 *
 * Why not just a bigger buffer: a sustained rate deficit eats any
 * fixed bank (the measured stretch is short about 26,000 samples,
 * over 800 ms of audio), and banking that much in steady state would
 * delay every sound by the same amount. The cushion instead defends
 * the bank that already exists: a streaming resampler sits between
 * the drained PCM and the track, and while production runs behind it
 * stretches the stream (more output frames per input frame, which is
 * a slight pitch/tempo drop) so consumption slows to match
 * production and the track never runs dry. When frames recover, the
 * stretch eases back to exactly 1.0, where the resampler is a
 * bit-exact pass-through: no sample is altered in steady state, the
 * lead target sits under the existing audio-gate cap, and steady
 * state sound delay is unchanged. Only the presentation layer is
 * involved; the core's production, content, saves and netplay state
 * are untouched.
 *
 * The controller is a band regulator on the track lead (frames the
 * track has accepted minus frames the DAC has played, the same
 * figure the game loop's audio gate uses). Below the band the
 * stretch pushes up, above it the stretch eases off, inside it the
 * stretch glides slowly back to 1.0. The band sits below the gate
 * cap on purpose: the gate's own sawtooth parks the average lead a
 * few hundred frames under the cap, and a point target at the cap
 * would fight that sawtooth. Constants were tuned against a
 * host-side simulation of this exact pipeline (game loop pacing,
 * gate, drain, track, DAC) driven by the TV's measured frame times:
 * baseline starved about 29,600 samples (roughly 0.93 s of audio,
 * in about 970 bursts) across the file-select stretch; with the
 * cushion it starved zero, holding at least ~700 frames of lead
 * throughout, at an average stretch of about 1.05 while engaged.
 * The cushion fully covers production deficits up to about 7.4
 * percent (the S_MAX clamp); beyond that it still stretches the
 * bank proportionally (an 18.5 ms/frame probe cut starvation from
 * about 2,210 ms to about 400 ms) but cannot hold the stream alone.
 *
 * Diagnostics: engaging and releasing are logged with the lead and
 * stretch, underrun episodes are counted (with an estimate of the
 * starved time) and logged throttled, and a session summary lands
 * when the stretch releases or the cushion is reset after use.
 */
class AudioCushion {

    companion object {
        private const val TAG = "iTaKEmu"

        // Controller constants, in frames at the core's sample rate
        // (the tuned case is SNES: 32,000 Hz, gate cap 2400 frames).
        //
        // 1.81 retune. The band has to clear the game loop's
        // production lead sawtooth, and the sawtooth is one frame of
        // production deep: 532 samples at the SNES rate (trough near
        // 1,868) but about 736 at the 44,100 Hz the other five cores
        // run at (trough near 1,664, sampled as low as 1,650 on the
        // near-budget trace). The shipped band bottom (2,100 - 200 =
        // 1,900) sat inside the 44,100 Hz sawtooth, so healthy NES,
        // Game Boy and PS1 play tripped the correction and left the
        // bit-exact path (stretch up to 1.0334 near budget on NES).
        // A band bottom of 1,500 (1,800 - 300) clears every shipped
        // core's trough. The six per-engine passes plus the combined
        // matrix (build server: itak-speed/audio-cushion-*) verify
        // every healthy trace back on the exact 1.0 path with zero
        // engagement and zero starved samples on every deficit trace
        // in the coverage class, the SNES file select included
        // (minimum lead about 700 frames, average stretch about 1.05
        // while engaged). The clamp rose from 1.06 to 1.08 because a
        // sustained 5 percent deficit's jitter peaks (about 6.9
        // percent instantaneous) exceed what 1.06 covers (5.7
        // percent); the PS1 5 percent trace is the one that forces
        // it. Riding the clamp costs about 1.3 semitones of pitch
        // against 1.0 at the old clamp, and only while a screen is
        // that far behind.
        private const val LEAD_TARGET = 1800.0   // band center
        private const val LEAD_BAND = 300.0      // band half-width
        private const val GAIN_UP = 0.8          // push-up rate, 1/s
        private const val GAIN_DN = 0.25         // ease-off rate, 1/s
        private const val LEAK = 0.04            // glide to 1.0 in band, 1/s
        private const val STRETCH_MAX = 1.08     // hardest stretch
        private const val STRETCH_SLEW = 0.10    // max |dstretch/dt|, 1/s

        // A stretch this close to 1.0 counts as released: at 32 kHz
        // it is a fraction of a percent of pitch, and parking the
        // controller exactly on 1.0 is what makes steady state
        // bit-exact again.
        private const val ENGAGE_AT = 1.002
        private const val RELEASE_AT = 1.0005

        // Runaway guard on the source queue, the same numbers the
        // audio thread always used: past 128 KB queued, keep only
        // the newest 64 KB. Production is gated by the track lead,
        // so reaching this means something upstream is deeply wrong;
        // dropping the oldest audio beats OOMing the screen.
        private const val QUEUE_MAX_BYTES = 131072
        private const val QUEUE_KEEP_BYTES = 65536

        private const val BYTES_PER_FRAME = 4    // stereo int16
    }

    // Source queue: drained PCM waiting for conversion.
    private var src = ByteArray(QUEUE_MAX_BYTES * 2)
    private var srcHead = 0          // frame index of the queue head
    private var srcLen = 0           // frames currently queued

    // Resampler read position, in frames relative to srcHead.
    private var pos = 0.0

    // Controller state.
    private var stretch = 1.0
    private var lastUpdateNs = 0L
    private var engaged = false
    private var engagedNs = 0L
    private var peakStretch = 1.0

    // Underrun accounting: an underrun is the DAC demanding samples
    // while the track lead is empty and the cushion has nothing to
    // convert. Armed only after the first real chunk flows, so the
    // track's startup transient never counts.
    private var armed = false
    private var starving = false
    private var underrunEpisodes = 0L
    private var starvedMsEstimate = 0.0
    private var lastUnderrunLogNs = 0L

    private var guardTrims = 0L

    /** Current stretch factor (1.0 = pass-through). */
    val currentStretch: Double
        get() = stretch

    /** Drop all queued audio and return the controller to rest. */
    fun reset() {
        if (engaged || peakStretch > 1.0 || underrunEpisodes > 0) {
            logSummary("reset")
        }
        srcHead = 0
        srcLen = 0
        pos = 0.0
        stretch = 1.0
        lastUpdateNs = 0L
        engaged = false
        peakStretch = 1.0
        armed = false
        starving = false
        underrunEpisodes = 0
        starvedMsEstimate = 0.0
        lastUnderrunLogNs = 0L
        guardTrims = 0
    }

    /** Queue drained PCM (little-endian stereo int16) for conversion. */
    fun feed(chunk: ByteArray) {
        if (chunk.isEmpty()) return
        val frames = chunk.size / BYTES_PER_FRAME
        if (frames == 0) return
        ensureCapacity(frames)
        System.arraycopy(chunk, 0, src, (srcHead + srcLen) * BYTES_PER_FRAME, frames * BYTES_PER_FRAME)
        srcLen += frames
        if (srcLen * BYTES_PER_FRAME > QUEUE_MAX_BYTES) {
            val keep = QUEUE_KEEP_BYTES / BYTES_PER_FRAME
            val drop = srcLen - keep
            srcHead += drop
            srcLen = keep
            // The frames the read position pointed at just moved
            // down by `drop`; shift pos with them (clamped: if it
            // pointed into the dropped span, restart at the head).
            pos = maxOf(0.0, pos - drop)
            guardTrims++
            if (guardTrims == 1L) {
                Log.w(TAG, "audio cushion: source queue runaway, trimmed to newest ${QUEUE_KEEP_BYTES} bytes")
            }
        }
    }

    /**
     * Advance the controller. Called on every audio-thread pass with
     * the current track lead (accepted minus played frames), so the
     * gains work in real time regardless of the pass cadence.
     */
    fun update(leadFrames: Long, nowNs: Long) {
        if (lastUpdateNs == 0L) {
            lastUpdateNs = nowNs
            return
        }
        // Cap dt: a pause or a stalled thread must not let the
        // controller integrate a huge step on resume. The slew clamp
        // below already bounds the visible effect; this keeps the
        // underrun estimate honest too.
        val dt = ((nowNs - lastUpdateNs).coerceAtMost(100_000_000L)) / 1e9
        lastUpdateNs = nowNs
        if (dt <= 0.0) return

        val err = LEAD_TARGET - leadFrames
        val rate = when {
            err > LEAD_BAND -> GAIN_UP * err / LEAD_TARGET
            err < -LEAD_BAND -> GAIN_DN * err / LEAD_TARGET - LEAK * (stretch - 1.0)
            else -> -LEAK * (stretch - 1.0)
        }
        var ds = rate * dt
        val maxStep = STRETCH_SLEW * dt
        if (ds > maxStep) ds = maxStep
        if (ds < -maxStep) ds = -maxStep
        val was = stretch
        stretch = (stretch + ds).coerceIn(1.0, STRETCH_MAX)
        if (stretch > peakStretch) peakStretch = stretch

        if (!engaged && stretch >= ENGAGE_AT) {
            engaged = true
            engagedNs = nowNs
            Log.i(TAG, "audio cushion engaged: lead=$leadFrames stretch=${fmt(stretch)}")
        } else if (engaged && stretch <= RELEASE_AT) {
            engaged = false
            stretch = 1.0
            // Returning to exactly 1.0: snap the read position to a
            // whole frame (a slip of at most half a frame, once) so
            // pass-through from here is bit-exact.
            pos = floor(pos + 0.5)
            logSummary("released")
        } else if (stretch == 1.0 && was > 1.0) {
            pos = floor(pos + 0.5)
        }

        // Underrun accounting.
        val empty = leadFrames <= 0 && srcLen < 2
        if (armed && empty) {
            starvedMsEstimate += dt * 1000.0
            if (!starving) {
                starving = true
                underrunEpisodes++
                if (underrunEpisodes == 1L || nowNs - lastUnderrunLogNs > 5_000_000_000L) {
                    lastUnderrunLogNs = nowNs
                    Log.w(TAG, "audio cushion: underrun, track starved (episode $underrunEpisodes, stretch=${fmt(stretch)})")
                }
            }
        } else {
            starving = false
        }
    }

    /**
     * Convert all queued source frames at the current stretch and
     * return the output PCM (possibly empty). One source frame is
     * always held back as the interpolation lookahead; it converts
     * on a later call once a successor exists.
     */
    fun convert(): ByteArray {
        if (srcLen < 2) return ByteArray(0)
        val estFrames = ((srcLen - 1) * stretch).toInt() + 2
        val out = ByteArray(estFrames * BYTES_PER_FRAME)
        var outFrames = 0
        val step = 1.0 / stretch
        while (floor(pos).toInt() + 1 < srcLen) {
            val i0 = floor(pos).toInt()
            val frac = pos - i0
            val o = outFrames * BYTES_PER_FRAME
            if (frac == 0.0) {
                // Whole-frame read: copy verbatim. This is also the
                // steady-state path (stretch parked at exactly 1.0),
                // which is what makes pass-through bit-exact.
                System.arraycopy(src, (srcHead + i0) * BYTES_PER_FRAME, out, o, BYTES_PER_FRAME)
            } else {
                for (ch in 0..1) {
                    val a = sampleAt(i0, ch)
                    val b = sampleAt(i0 + 1, ch)
                    val v = (a + (b - a) * frac).toInt()
                    val off = o + ch * 2
                    out[off] = (v and 0xFF).toByte()
                    out[off + 1] = ((v shr 8) and 0xFF).toByte()
                }
            }
            pos += step
            outFrames++
        }
        val drop = floor(pos).toInt()
        if (drop > 0) {
            srcHead += drop
            srcLen -= drop
            pos -= drop
            compactIfNeeded()
        }
        if (outFrames > 0) armed = true
        return if (outFrames == estFrames) out else out.copyOf(outFrames * BYTES_PER_FRAME)
    }

    private fun sampleAt(frame: Int, ch: Int): Int {
        val off = (srcHead + frame) * BYTES_PER_FRAME + ch * 2
        return (src[off + 1].toInt() shl 8) or (src[off].toInt() and 0xFF)
    }

    private fun ensureCapacity(addFrames: Int) {
        val need = (srcHead + srcLen + addFrames) * BYTES_PER_FRAME
        if (need <= src.size) return
        val remaining = srcLen * BYTES_PER_FRAME
        if ((srcLen + addFrames) * BYTES_PER_FRAME <= src.size) {
            // Fits once the consumed head space is reclaimed.
            System.arraycopy(src, srcHead * BYTES_PER_FRAME, src, 0, remaining)
        } else {
            val bigger = ByteArray(maxOf(src.size * 2, (srcLen + addFrames) * BYTES_PER_FRAME))
            System.arraycopy(src, srcHead * BYTES_PER_FRAME, bigger, 0, remaining)
            src = bigger
        }
        srcHead = 0
        // pos is relative to srcHead and the frames it points at
        // moved with the copy, so pos itself is unchanged.
    }

    private fun compactIfNeeded() {
        if (srcHead < 16384) return
        val remaining = srcLen * BYTES_PER_FRAME
        System.arraycopy(src, srcHead * BYTES_PER_FRAME, src, 0, remaining)
        srcHead = 0
        // pos is relative to srcHead; the frames it pointed at moved
        // with the copy, so pos itself is unchanged.
    }

    private fun logSummary(reason: String) {
        val engagedS = if (engagedNs > 0) (lastUpdateNs - engagedNs) / 1e9 else 0.0
        Log.i(
            TAG,
            "audio cushion $reason: peak stretch=${fmt(peakStretch)}" +
                " engaged=${"%.1f".format(engagedS)}s underruns=$underrunEpisodes" +
                " starved~${starvedMsEstimate.toInt()}ms guardTrims=$guardTrims"
        )
        engagedNs = 0L
        peakStretch = stretch
        underrunEpisodes = 0
        starvedMsEstimate = 0.0
    }

    private fun fmt(v: Double): String = "%.4f".format(v)
}
