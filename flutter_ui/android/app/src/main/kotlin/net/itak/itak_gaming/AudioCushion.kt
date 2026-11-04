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
 * The controller works on the track lead (frames the track has
 * accepted minus frames the DAC has played, the same figure the
 * game loop's audio gate uses).
 *
 * Controller law (1.96, replaces the band regulator). The shipped
 * band law integrated the lead error with fixed gains, and a real
 * session on David's TV (2026-11-03, Zelda intro) showed the
 * result: the stretch hunted between about 1.02 and the 1.08
 * clamp in a roughly four second cycle while the true deficit
 * only needed about 1.03, a slow pitch warble with almost no
 * actual starvation. The new law is a proportional tracker plus
 * a fast guard. A one second mean of the lead maps linearly to
 * the target stretch: at the 1,750 frame setpoint or above it
 * the target is exactly 1.0, and the further the mean sits below
 * the setpoint, the more stretch the target asks for. The
 * operating point is a true equilibrium (at the matching stretch
 * the lead holds at the crossing), and the stretch glides toward
 * the target at 0.01 to 0.03 per second, so the pitch parks
 * instead of hunting. While the instantaneous lead is under 900
 * frames the guard takes over at the old push-up rate, bounded
 * by the tracker target plus a small margin so a scene step
 * cannot overshoot to the clamp; under 350 frames the bound
 * lifts and each pass steps the stretch directly.
 *
 * Host simulation of this exact pipeline (game loop pacing,
 * gate, drain, track, DAC; sim2 in the cushion retune rig)
 * across healthy, steady 3 percent, file-select (4.5 percent
 * for 21 s), scene-step, breathing, and 7.5 percent traces, at
 * both the 32,040 Hz and 44,100 Hz shapes: healthy play never
 * engages under either law; the tracker parks a steady 3
 * percent deficit at 1.0309 against a theoretical need of 1.031
 * with a stretch wander of 0.0002 (the band law: wander 0.032,
 * parked high at about 1.04 to 1.06) and a pitch movement rate
 * ten times slower; zero starved on every trace the band law
 * passed. At a sustained 7.5 percent deficit both laws sit at
 * the clamp and starve the same small amount: that is past what
 * any 8 percent cushion can cover, and it is core speed, not
 * tuning. The original no-cushion baseline on the file-select
 * trace starved about 29,600 samples (roughly 0.93 s of audio);
 * with the cushion it starves zero.
 *
 * Diagnostics: engaging and releasing are logged with the lead and
 * stretch, underrun episodes are counted (with an estimate of the
 * starved time) and logged throttled, and a session summary lands
 * when the stretch releases or the cushion is reset after use.
 *
 * Two roles since 1.95. On the legacy path (cores whose track
 * opens at the core rate) this class is converter and controller
 * together, exactly as above: feed(), update(), convert(). On the
 * resampled path (SNES, GBA) it is the controller only: the audio
 * thread calls update() with the resampler's backlog as
 * queuedFrames and hands currentStretch to AudioResampler, which
 * applies the stretch inside its sinc conversion. The linear
 * interpolation in convert() below no longer touches the SNES or
 * GBA stream at all.
 */
class AudioCushion {

    companion object {
        private const val TAG = "iTaKEmu"

        // Controller constants, in frames at the core's sample rate
        // (the tuned case is SNES: 32,040 Hz, gate cap 2400 frames).
        //
        // Tracker law (1.96, see the class doc for the evidence).
        // The setpoint sits below every shipped core's healthy
        // sawtooth mean (the gate parks the lead between the cap
        // and one chunk under it: means near 2,130 at the SNES
        // shape, near 2,030 at the 44,100 Hz shapes), so healthy
        // play computes a target of exactly 1.0 and never engages,
        // the same purity the 1.81 band retune was built to win.
        // The gain maps a full setpoint of mean lead error to
        // 0.12 of stretch, which puts the equilibrium lead for a
        // sustained deficit inside the bank (about 1,300 frames
        // at 3 percent, about 1,060 at 4.5 percent) with the guard
        // thresholds below it. The clamp stays 1.08: a sustained
        // 5 percent deficit's jitter peaks (about 6.9 percent
        // instantaneous) exceed what 1.06 covers.
        private const val LEAD_TARGET = 1750.0   // tracker setpoint (also the diag target)
        private const val MEAN_TAU_S = 1.0       // lead mean time constant, s
        private const val TRACK_GAIN = 0.12      // stretch per full setpoint of mean error
        private const val SLEW = 0.01            // tracker glide, 1/s
        private const val SLEW_NEAR = 0.03       // tracker glide while the lead is low, 1/s
        private const val NEAR_LEAD = 1500.0     // below this the tracker glides faster
        private const val GUARD_LEAD = 900.0     // guard engages below this lead
        private const val GUARD_EXIT = 1100.0    // and holds until the lead recovers past this
        private const val GUARD_RATE = 0.10      // guard push-up rate, 1/s (the shipped slew)
        private const val GUARD_MARGIN = 0.01    // guard may exceed the tracker target by this
        private const val CRIT_LEAD = 350.0      // emergency: bound lifts, direct step per pass
        private const val CRIT_STEP = 0.02       // emergency step per update pass
        private const val STRETCH_MAX = 1.08     // hardest stretch

        // A stretch this close to 1.0 counts as released: at 32 kHz
        // it is a fraction of a percent of pitch, and parking the
        // controller exactly on 1.0 is what makes steady state
        // bit-exact again.
        private const val ENGAGE_AT = 1.002
        private const val RELEASE_AT = 1.0005

        // Release dwell (1.85). A real session on the Missouri
        // stick (2026-11-02) showed the controller engaging and
        // releasing about a dozen times in 14 seconds while the
        // lead hovered at the band edge: each cycle is a small
        // pitch wobble. After a release, the stretch is capped at
        // DWELL_STRETCH_CAP (a tenth of a percent, about 2 cents
        // of pitch, inaudible) for RELEASE_DWELL_NS, so band-edge
        // noise cannot immediately re-engage it. The cap lifts
        // early if the lead falls to DWELL_EMERGENCY_LEAD, so a
        // genuine deficit still engages at once. Host simulation
        // (completed 2026-11-02, after the 1.85 ship; DWELL.md in
        // the cushion rig): the file-select trace is numerically
        // identical with the dwell on, zero starved, and a
        // retrigger inside the window is capped at 1.001, which
        // halves the engagement rate on periodic chatter.
        private const val RELEASE_DWELL_NS = 250_000_000L
        private const val DWELL_STRETCH_CAP = 1.001
        private const val DWELL_EMERGENCY_LEAD = 1200.0 // dwell lifts early below this lead

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
    private var lastReleaseNs = 0L

    // Tracker state (1.96): the lead mean and the guard latch.
    private var meanLead = 0.0
    private var hasMean = false
    private var guarded = false

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

    // Session-cumulative mirrors of the episodic counters above.
    // logSummary() zeroes the episodic pair at every release, which
    // is right for log lines but wrong for an end-of-session total;
    // these accumulate across the whole session and are cleared
    // only by reset(). Read them through diagTotals().
    private var sessionUnderrunEpisodes = 0L
    private var sessionStarvedMs = 0.0
    private var sessionPeakStretch = 1.0

    /** Snapshot of the session-cumulative counters, for AudioDiag. */
    data class SessionTotals(
        val underrunEpisodes: Long,
        val starvedMs: Double,
        val guardTrims: Long,
        val peakStretch: Double,
    )

    /** The controller's lead target, in core-rate frames. */
    fun diagLeadTargetFrames(): Double = LEAD_TARGET

    /** The session totals as they stand right now; never resets. */
    fun diagTotals(): SessionTotals = SessionTotals(
        underrunEpisodes = sessionUnderrunEpisodes,
        starvedMs = sessionStarvedMs,
        guardTrims = guardTrims,
        peakStretch = sessionPeakStretch,
    )

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
        lastReleaseNs = 0L
        meanLead = 0.0
        hasMean = false
        guarded = false
        armed = false
        starving = false
        underrunEpisodes = 0
        starvedMsEstimate = 0.0
        lastUnderrunLogNs = 0L
        guardTrims = 0
        sessionUnderrunEpisodes = 0
        sessionStarvedMs = 0.0
        sessionPeakStretch = 1.0
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
     *
     * queuedFrames is the conversion backlog the underrun test
     * should look at: on the legacy path the caller leaves the
     * default and this class's own queue is used; on the resampled
     * path (controller-only role) the caller passes the
     * resampler's backlog instead.
     */
    fun update(leadFrames: Long, nowNs: Long, queuedFrames: Int = -1) {
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

        // Tracker (1.96): the target stretch comes from the mean
        // lead. At the setpoint or above it the target is exactly
        // 1.0; the further the mean sits below the setpoint, the
        // more stretch the target asks for, and the operating point
        // is a true equilibrium (at the matching stretch the lead
        // holds at the crossing), so the pitch parks at the need
        // instead of hunting around it.
        if (!hasMean) {
            meanLead = leadFrames.toDouble()
            hasMean = true
        } else {
            meanLead += (dt / MEAN_TAU_S) * (leadFrames - meanLead)
        }
        val errFrac = ((LEAD_TARGET - meanLead) / LEAD_TARGET).coerceIn(0.0, 1.0)
        val target = (1.0 + errFrac * TRACK_GAIN).coerceIn(1.0, STRETCH_MAX)

        if (leadFrames < GUARD_LEAD) guarded = true
        else if (leadFrames > GUARD_EXIT) guarded = false

        val was = stretch
        if (guarded) {
            // Fast guard: the old push-up rate, bounded by the
            // tracker target plus a small margin so a scene step
            // cannot overshoot to the clamp. Below CRIT_LEAD the
            // bound lifts and each pass steps the stretch directly.
            val capNow = if (leadFrames < CRIT_LEAD) STRETCH_MAX else target + GUARD_MARGIN
            if (leadFrames < CRIT_LEAD) {
                stretch = (stretch + CRIT_STEP).coerceAtMost(STRETCH_MAX)
            }
            stretch = (stretch + GUARD_RATE * dt).coerceAtMost(capNow)
            if (stretch > target && leadFrames > GUARD_LEAD) {
                stretch = (stretch - SLEW * dt).coerceAtLeast(target)
            }
        } else if (stretch < target) {
            val slew = if (leadFrames < NEAR_LEAD) SLEW_NEAR else SLEW
            stretch = (stretch + slew * dt).coerceAtMost(target)
        } else {
            stretch = (stretch - SLEW * dt).coerceAtLeast(target)
        }
        stretch = stretch.coerceIn(1.0, STRETCH_MAX)
        // Release dwell: hold the stretch at the inaudible cap for
        // a beat after a release, unless the lead says the deficit
        // is real (see the constants for the evidence).
        if (nowNs - lastReleaseNs < RELEASE_DWELL_NS &&
            leadFrames >= DWELL_EMERGENCY_LEAD &&
            stretch > DWELL_STRETCH_CAP
        ) {
            stretch = DWELL_STRETCH_CAP
        }
        if (stretch > peakStretch) peakStretch = stretch
        if (stretch > sessionPeakStretch) sessionPeakStretch = stretch

        if (!engaged && stretch >= ENGAGE_AT) {
            engaged = true
            engagedNs = nowNs
            Log.i(TAG, "audio cushion engaged: lead=$leadFrames stretch=${fmt(stretch)}")
        } else if (engaged && stretch <= RELEASE_AT) {
            engaged = false
            stretch = 1.0
            lastReleaseNs = nowNs
            // Returning to exactly 1.0: snap the read position to a
            // whole frame (a slip of at most half a frame, once) so
            // pass-through from here is bit-exact.
            pos = floor(pos + 0.5)
            logSummary("released")
        } else if (stretch == 1.0 && was > 1.0) {
            pos = floor(pos + 0.5)
        }

        // A positive lead means converted audio reached the track,
        // which is the same flow convert() arms on; saying so here
        // keeps the accounting armed on the controller-only path
        // too, where convert() never runs.
        if (leadFrames > 0) armed = true

        // Underrun accounting.
        val queued = if (queuedFrames >= 0) queuedFrames else srcLen
        val empty = leadFrames <= 0 && queued < 2
        if (armed && empty) {
            starvedMsEstimate += dt * 1000.0
            sessionStarvedMs += dt * 1000.0
            if (!starving) {
                starving = true
                underrunEpisodes++
                sessionUnderrunEpisodes++
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
