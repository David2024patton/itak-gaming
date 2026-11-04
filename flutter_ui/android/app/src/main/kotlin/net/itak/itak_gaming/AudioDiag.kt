package net.itak.itak_gaming

import android.content.Context
import android.util.Log
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * Audio session diagnostics: the measuring half of the sound
 * debugging set (ordered by David 2026-11-02, after a Zelda SNES
 * session where the sound audibly lagged the picture and the
 * existing debug report could prove the stream never starved but
 * could not say how late it was).
 *
 * What it adds over the log lines the path already emits:
 *
 * - A session header: which core and game, the core rate, the
 *   track rate, whether the in-app resampler is in the path, the
 *   track's buffer as actually built (frames and milliseconds),
 *   the cushion target in milliseconds, the device output rate,
 *   and the output device the track was routed to.
 * - A measured output latency series, sampled from the track's
 *   own presentation timestamps while the session runs: how many
 *   frames sit between what the app has written and what the
 *   device says it has presented, in milliseconds. This is the
 *   delay figure the old report could only be guessed at by
 *   adding buffer sizes together.
 * - A lead trace: the same track lead the cushion regulates,
 *   bucketed over the session (min / average / max per bucket,
 *   in milliseconds), so reserve depth is a record instead of
 *   an inference from engage and release lines.
 * - A stretch histogram: how many seconds the session spent at
 *   each cushion stretch band, plus the cushion's own session
 *   totals (underruns, starved time, guard trims) taken at the
 *   end without being reset by the per-engagement summaries.
 *
 * The finished summary is a JSON object. EmulatorActivity saves
 * it to disk at session end; DebugChute attaches the most recent
 * one to every full report under the key "audioDiag", so the
 * numbers ride the chute that already exists instead of needing
 * a new channel.
 *
 * Threading: begin() is called before the audio thread starts,
 * sample() only from the audio thread, finish() after the game
 * thread has been joined. Methods are synchronized anyway; the
 * sampling cadence is a few times a second, so contention is
 * not a consideration.
 */
class AudioDiag {

    companion object {
        private const val TAG = "iTaKEmu"
        private const val DIR = "diag"
        private const val FILE = "audio_last.json"

        // Lead buckets: one per 5 seconds of session, the last
        // 36 kept (3 minutes). Older buckets are folded into the
        // session-wide lead statistics, which are always kept.
        private const val BUCKET_NS = 5_000_000_000L
        private const val MAX_BUCKETS = 36

        /** Where the last finished session summary lives. */
        fun storeFile(ctx: Context): File = File(File(ctx.filesDir, DIR), FILE)

        /** Save a finished session summary for the chute. */
        fun saveLast(ctx: Context, summary: JSONObject) {
            try {
                val file = storeFile(ctx)
                file.parentFile?.mkdirs()
                file.writeText(summary.toString())
            } catch (e: Exception) {
                Log.w(TAG, "audio diag save failed", e)
            }
        }

        /** The most recent finished session summary, if any. */
        fun loadLast(ctx: Context): JSONObject? {
            return try {
                val file = storeFile(ctx)
                if (file.exists()) JSONObject(file.readText()) else null
            } catch (e: Exception) {
                Log.w(TAG, "audio diag load failed", e)
                null
            }
        }
    }

    // ---- session header -------------------------------------------------
    private var active = false
    private var begun = false
    private var startedNs = 0L
    private var system = ""
    private var game = ""
    private var coreRate = 0
    private var trackRate = 0
    private var resamplerInPath = false
    private var trackBufferFrames = 0
    private var cushionTargetMs = 0.0
    private var deviceOutputRate = 0
    private var routedDevice = ""

    // ---- lead (reserve depth), milliseconds at the track rate -----------
    private var leadMin = Double.MAX_VALUE
    private var leadMax = 0.0
    private var leadSum = 0.0
    private var leadCount = 0L
    private var bucketStartNs = 0L
    private var bucketMin = Double.MAX_VALUE
    private var bucketMax = 0.0
    private var bucketSum = 0.0
    private var bucketCount = 0L
    private val buckets = ArrayDeque<JSONArray>()

    // ---- measured output latency, milliseconds --------------------------
    private var latMin = Double.MAX_VALUE
    private var latMax = 0.0
    private var latSum = 0.0
    private var latCount = 0L
    private var latLast = 0.0

    // ---- stretch histogram, seconds per band ----------------------------
    // bands: exactly 1.0 / (1.0, 1.02] / (1.02, 1.05] / (1.05, 1.08]
    private val stretchBandsS = DoubleArray(4)
    private var lastSampleNs = 0L

    /**
     * Open a session. [trackBufferFrames] is the size the built
     * track reports for itself, not the requested size; pass 0
     * when the track could not report one.
     */
    @Synchronized
    fun begin(
        system: String,
        game: String,
        coreRate: Int,
        trackRate: Int,
        resamplerInPath: Boolean,
        trackBufferFrames: Int,
        cushionTargetMs: Double,
        deviceOutputRate: Int,
        routedDevice: String,
    ) {
        this.system = system
        this.game = game
        this.coreRate = coreRate
        this.trackRate = trackRate
        this.resamplerInPath = resamplerInPath
        this.trackBufferFrames = trackBufferFrames
        this.cushionTargetMs = cushionTargetMs
        this.deviceOutputRate = deviceOutputRate
        this.routedDevice = routedDevice
        startedNs = System.nanoTime()
        bucketStartNs = startedNs
        lastSampleNs = startedNs
        active = true
        begun = true
        Log.i(
            TAG,
            "audio session: system=$system game=$game core=${coreRate}Hz" +
                " track=${trackRate}Hz resampler=${if (resamplerInPath) "in-app" else "off"}" +
                " trackBuffer=${trackBufferFrames}f" +
                " (${if (trackRate > 0) "%.1f".format(trackBufferFrames * 1000.0 / trackRate) else "?"}ms)" +
                " cushionTarget=${"%.1f".format(cushionTargetMs)}ms" +
                " output=${deviceOutputRate}Hz routed=$routedDevice"
        )
    }

    /**
     * One sample from the audio thread. [leadMs] is the track
     * lead (written minus presented frames) in milliseconds,
     * [latencyMs] the timestamp-measured output latency in
     * milliseconds (pass a negative value when the timestamp
     * call failed, the sample is then skipped), [stretch] the
     * cushion's current stretch factor.
     */
    @Synchronized
    fun sample(leadMs: Double, latencyMs: Double, stretch: Double, nowNs: Long) {
        if (!active) return
        // Lead, session-wide and per bucket.
        leadMin = minOf(leadMin, leadMs)
        leadMax = maxOf(leadMax, leadMs)
        leadSum += leadMs
        leadCount++
        if (bucketCount > 0 && nowNs - bucketStartNs >= BUCKET_NS) {
            val b = JSONArray()
            b.put((bucketStartNs - startedNs) / 1_000_000_000.0)
            b.put(bucketMin)
            b.put(bucketSum / bucketCount)
            b.put(bucketMax)
            buckets.addLast(b)
            while (buckets.size > MAX_BUCKETS) buckets.removeFirst()
            bucketStartNs = nowNs
            bucketMin = Double.MAX_VALUE
            bucketMax = 0.0
            bucketSum = 0.0
            bucketCount = 0
        }
        bucketMin = minOf(bucketMin, leadMs)
        bucketMax = maxOf(bucketMax, leadMs)
        bucketSum += leadMs
        bucketCount++
        // Latency, when the sample is real.
        if (latencyMs >= 0.0) {
            latMin = minOf(latMin, latencyMs)
            latMax = maxOf(latMax, latencyMs)
            latSum += latencyMs
            latCount++
            latLast = latencyMs
        }
        // Stretch histogram: credit the time since the previous
        // sample to the band the stretch sits in now.
        val dtS = (nowNs - lastSampleNs) / 1e9
        if (dtS > 0.0 && dtS < 2.0) {
            val band = when {
                stretch <= 1.0005 -> 0
                stretch <= 1.02 -> 1
                stretch <= 1.05 -> 2
                else -> 3
            }
            stretchBandsS[band] += dtS
        }
        lastSampleNs = nowNs
    }

    /**
     * Close the session and build its summary. The cushion
     * totals are passed in by the caller (they live in the
     * cushion); [framesDelivered] is the audio thread's own
     * count of frames the track accepted.
     */
    @Synchronized
    fun finish(
        underrunEpisodes: Long,
        starvedMs: Double,
        guardTrims: Long,
        peakStretch: Double,
        framesDelivered: Long,
        outputTrims: Long,
        outputTrimmedFrames: Long,
    ): JSONObject {
        active = false
        val o = JSONObject()
        // False when the session never got a track (the core
        // reported no rate): the summary then documents the
        // failure instead of a measurement.
        o.put("sessionBegun", begun)
        o.put("system", system)
        o.put("game", game)
        o.put("coreRate", coreRate)
        o.put("trackRate", trackRate)
        o.put("resamplerInPath", resamplerInPath)
        o.put("trackBufferFrames", trackBufferFrames)
        if (trackRate > 0) {
            o.put("trackBufferMs", trackBufferFrames * 1000.0 / trackRate)
        }
        o.put("cushionTargetMs", cushionTargetMs)
        o.put("deviceOutputRate", deviceOutputRate)
        o.put("routedDevice", routedDevice)
        o.put("durationS", (System.nanoTime() - startedNs) / 1e9)
        o.put("framesDelivered", framesDelivered)
        if (leadCount > 0) {
            val lead = JSONObject()
            lead.put("minMs", leadMin)
            lead.put("avgMs", leadSum / leadCount)
            lead.put("maxMs", leadMax)
            o.put("lead", lead)
        }
        if (buckets.isNotEmpty() || bucketCount > 0) {
            val arr = JSONArray()
            for (b in buckets) arr.put(b)
            if (bucketCount > 0) {
                val b = JSONArray()
                b.put((bucketStartNs - startedNs) / 1_000_000_000.0)
                b.put(bucketMin)
                b.put(bucketSum / bucketCount)
                b.put(bucketMax)
                arr.put(b)
            }
            // Each bucket: [startSecond, minMs, avgMs, maxMs].
            o.put("leadBuckets", arr)
        }
        if (latCount > 0) {
            val lat = JSONObject()
            lat.put("minMs", latMin)
            lat.put("avgMs", latSum / latCount)
            lat.put("maxMs", latMax)
            lat.put("lastMs", latLast)
            lat.put("samples", latCount)
            o.put("outputLatency", lat)
        }
        val bands = JSONArray()
        for (v in stretchBandsS) bands.put(v)
        o.put("stretchSecondsByBand", bands)
        o.put("peakStretch", peakStretch)
        o.put("underrunEpisodes", underrunEpisodes)
        o.put("starvedMs", starvedMs)
        o.put("guardTrims", guardTrims)
        o.put("outputTrims", outputTrims)
        o.put("outputTrimmedFrames", outputTrimmedFrames)
        Log.i(
            TAG,
            "audio session end: game=$game" +
                (if (latCount > 0) " latency avg=${"%.1f".format(latSum / latCount)}ms" +
                    " max=${"%.1f".format(latMax)}ms" else " latency unmeasured") +
                (if (leadCount > 0) " lead avg=${"%.1f".format(leadSum / leadCount)}ms" else "") +
                " underruns=$underrunEpisodes starved~${starvedMs.toInt()}ms" +
                " outputTrims=$outputTrims"
        )
        return o
    }
}
