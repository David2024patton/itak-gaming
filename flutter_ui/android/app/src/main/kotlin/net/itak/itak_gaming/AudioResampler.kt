package net.itak.itak_gaming

import android.util.Log
import kotlin.math.PI
import kotlin.math.sin
import kotlin.math.sqrt

/**
 * The in-app sample rate converter: turns core-rate audio into the
 * device output rate inside the app, at a quality we choose.
 *
 * Why this exists. The SNES core produces audio at 32,000 Hz and the
 * GBA at 32,768 Hz, but the stick's output stage runs at 48,000 Hz.
 * Before 1.85 the app opened its audio track at the core rate and
 * let the stick's mixer do the conversion. That converter's quality
 * is the platform's choice, not ours: Android's own documentation
 * warns its resamplers can add aliasing noise, the quality rung a
 * given track gets depends on load and vendor settings, and no app
 * API can see or change it. Every respected SNES emulator instead
 * converts inside itself with a filter it picked. This module is
 * that conversion, ours.
 *
 * The algorithm is a table-driven windowed-sinc resampler, proven
 * on the build server before it was ported (the Python prototype
 * is the spec: itak-speed/resampler-proto/resample.py). For each
 * output sample, the position in input samples advances by the
 * exact rational step inRate/outRate using integer math only, so
 * there is zero drift. The kernel is 128 input taps of a Kaiser
 * windowed sinc (beta 7.9) with cutoff at 0.98 of the lower
 * Nyquist, quantized to 4096 phases; every table row is
 * renormalized to sum to exactly 1.0, so DC gain is exact at every
 * phase. One table serves every ratio because the kernel is
 * defined in input-sample units.
 *
 * Measured against the prototype (32,000 to 48,000): the clean
 * Zelda capture's band energies match the original within
 * 0.04 dB, image energy sits at about -80 dB, and on a harsh
 * synthetic signal (bright square arpeggio plus a 1 to 15 kHz
 * sweep) the error against the ideal is 65 to 72 dB lower than
 * linear interpolation or zero-order hold. The GBA's awkward
 * 375:256 ratio lands within about 2.5 dB of the SNES figures.
 * Cost is 128 multiply-accumulates per output sample per channel,
 * about 12 million a second for stereo at 48 kHz, plus the shared
 * 2 MiB table, generated once per process.
 *
 * Streaming: an output sample depends only on the 128 input
 * samples around its position and on the integer accumulator, so
 * the converter is chunk-safe by construction (the prototype
 * matched its offline path bit-for-bit fed in frame-sized
 * chunks). Algorithmic latency is 64 input samples of lookahead,
 * 2.0 ms at the SNES rate. Presentation layer only: the core's
 * production, saves, and netplay state are untouched.
 */
class AudioResampler(private val inRate: Int, private val outRate: Int) {

    companion object {
        private const val TAG = "iTaKEmu"

        // Port constants: copy from the proven prototype, do not
        // retune without re-running its proofs.
        const val PHASES = 4096
        const val TAPS = 128
        private const val HALF = TAPS / 2
        private const val CUTOFF = 0.98
        private const val KAISER_BETA = 7.9

        private const val BYTES_PER_FRAME = 4 // stereo int16

        /**
         * The shared kernel table, built once per process on first
         * use. Generation evaluates 4096 x 128 windowed sinc
         * coefficients in double precision, then stores floats.
         */
        val table: FloatArray by lazy {
            val started = System.nanoTime()
            val built = buildTable()
            Log.i(TAG, "audio resampler table built in ${(System.nanoTime() - started) / 1_000_000}ms")
            built
        }

        private fun buildTable(): FloatArray {
            val fc = 0.5 * CUTOFF // cycles per input sample
            val i0Beta = besselI0(KAISER_BETA)
            val out = FloatArray(PHASES * TAPS)
            val row = DoubleArray(TAPS)
            for (q in 0 until PHASES) {
                val frac = q.toDouble() / PHASES
                var sum = 0.0
                for (t in 0 until TAPS) {
                    val d = (t - HALF).toDouble()
                    val x = frac - d
                    // Kaiser window: I0(beta*sqrt(1-(x/half)^2))/I0(beta),
                    // the radicand clamped at 0 exactly as the
                    // prototype clamps it.
                    val rad = 1.0 - (x / HALF) * (x / HALF)
                    val w = besselI0(KAISER_BETA * sqrt(if (rad > 0.0) rad else 0.0)) / i0Beta
                    val s = 2.0 * fc * x
                    val sinc = if (s == 0.0) 1.0 else sin(PI * s) / (PI * s)
                    val h = 2.0 * fc * sinc * w
                    row[t] = h
                    sum += h
                }
                val base = q * TAPS
                for (t in 0 until TAPS) {
                    out[base + t] = (row[t] / sum).toFloat()
                }
            }
            return out
        }

        /** Modified Bessel function I0, power series. */
        private fun besselI0(x: Double): Double {
            val quarter = x * x / 4.0
            var sum = 1.0
            var term = 1.0
            var k = 0
            while (true) {
                k++
                term *= quarter / (k.toDouble() * k.toDouble())
                sum += term
                if (term < sum * 1e-15) break
            }
            return sum
        }
    }

    init {
        require(inRate > 0 && outRate > 0) { "resampler rates must be positive" }
    }

    // Input history: interleaved int16 frames in a linear buffer.
    // The stream is primed with HALF frames of silence standing in
    // for the input before frame 0, exactly the prototype's zero
    // padding, so the first outputs are computed, not skipped.
    private var buf = ShortArray((TAPS + 8192) * 2)
    private var head = 0 // frame index of the oldest buffered frame
    private var len = HALF // frames currently buffered
    private var baseIndex = -HALF.toLong() // absolute input index of the head frame

    // Exact rational position of the next output, as n * inRate.
    private var acc = 0L

    /** Drop all buffered input and restart the position. */
    fun reset() {
        head = 0
        len = HALF
        baseIndex = -HALF.toLong()
        acc = 0L
        buf.fill(0, 0, HALF * 2)
    }

    /**
     * Convert one chunk of core-rate PCM (little-endian stereo
     * int16) to the output rate, returning the converted bytes
     * (possibly empty while the lookahead fills). Chunk boundaries
     * are invisible to the result.
     */
    fun convert(chunk: ByteArray): ByteArray {
        if (chunk.isEmpty()) return ByteArray(0)
        val frames = chunk.size / BYTES_PER_FRAME
        if (frames == 0) return ByteArray(0)
        ensureCapacity(frames)
        var srcOff = 0
        var dst = (head + len) * 2
        for (f in 0 until frames) {
            buf[dst] = ((chunk[srcOff + 1].toInt() shl 8) or (chunk[srcOff].toInt() and 0xFF)).toShort()
            buf[dst + 1] = ((chunk[srcOff + 3].toInt() shl 8) or (chunk[srcOff + 2].toInt() and 0xFF)).toShort()
            srcOff += BYTES_PER_FRAME
            dst += 2
        }
        len += frames

        val lastAvailable = baseIndex + len - 1
        // Estimate the output size, then trim to what was produced.
        val estOut = ((len.toLong() * outRate) / inRate).toInt() + 2
        val out = ByteArray(estOut * BYTES_PER_FRAME)
        var outFrames = 0
        val tab = table
        while (true) {
            val i0 = acc / outRate
            // The window covers inputs [i0 - HALF, i0 + HALF); the
            // newest tap must already have arrived.
            if (i0 + HALF - 1 > lastAvailable) break
            val phase = (((acc % outRate) * PHASES) / outRate).toInt()
            val rowBase = phase * TAPS
            val winStart = (i0 - HALF - baseIndex).toInt() // buffer frame of the oldest tap
            var framePos = (head + winStart) * 2
            var sumL = 0f
            var sumR = 0f
            for (t in 0 until TAPS) {
                val c = tab[rowBase + t]
                sumL += buf[framePos] * c
                sumR += buf[framePos + 1] * c
                framePos += 2
            }
            val o = outFrames * BYTES_PER_FRAME
            val vl = Math.round(sumL).coerceIn(-32768, 32767)
            val vr = Math.round(sumR).coerceIn(-32768, 32767)
            out[o] = (vl and 0xFF).toByte()
            out[o + 1] = ((vl shr 8) and 0xFF).toByte()
            out[o + 2] = (vr and 0xFF).toByte()
            out[o + 3] = ((vr shr 8) and 0xFF).toByte()
            outFrames++
            acc += inRate
        }
        // Drop input frames no future window can reach: the next
        // window starts at (acc / outRate) - HALF.
        val nextStart = acc / outRate - HALF
        val drop = (nextStart - baseIndex).toInt()
        if (drop > 0) {
            val d = minOf(drop, len)
            head += d
            len -= d
            baseIndex += d
            compactIfNeeded()
        }
        return if (outFrames == estOut) out else out.copyOf(outFrames * BYTES_PER_FRAME)
    }

    private fun ensureCapacity(addFrames: Int) {
        val needFrames = head + len + addFrames
        if (needFrames * 2 <= buf.size) return
        val remaining = len * 2
        if ((len + addFrames) * 2 <= buf.size) {
            System.arraycopy(buf, head * 2, buf, 0, remaining)
        } else {
            val bigger = ShortArray(maxOf(buf.size * 2, (len + addFrames) * 2))
            System.arraycopy(buf, head * 2, bigger, 0, remaining)
            buf = bigger
        }
        head = 0
    }

    private fun compactIfNeeded() {
        if (head < 16384) return
        System.arraycopy(buf, head * 2, buf, 0, len * 2)
        head = 0
    }
}
