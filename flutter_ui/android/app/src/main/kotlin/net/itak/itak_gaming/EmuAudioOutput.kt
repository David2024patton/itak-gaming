package net.itak.itak_gaming

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioTrack
import android.util.Log

/**
 * The one place the app's game audio output is described, shared
 * by the emulator screen and the sound test screen so the test
 * exercises the real path instead of a copy of it.
 *
 * Two facts live here: the rate the device itself wants (its
 * native output rate, what the mixer runs at), and how a game
 * audio track is built (game usage, music content type, PCM16
 * stereo, streaming mode, the buffer policy the emulator has
 * always used).
 */
object EmuAudioOutput {

    private const val TAG = "iTaKEmu"

    /** Fallback when the device does not report a native rate. */
    private const val DEFAULT_OUTPUT_RATE = 48000

    /**
     * The device's native output sample rate: the rate its mixer
     * runs at, and the rate an app-side converter should target.
     * Falls back to 48,000 when the property is missing or absurd.
     */
    fun deviceOutputRate(context: Context): Int {
        val manager = context.getSystemService(Context.AUDIO_SERVICE) as? AudioManager
        val raw = try {
            manager?.getProperty(AudioManager.PROPERTY_OUTPUT_SAMPLE_RATE)
        } catch (e: Exception) {
            Log.w(TAG, "output sample rate lookup failed", e)
            null
        }
        val rate = raw?.toIntOrNull() ?: 0
        return if (rate in 8000..192000) rate else DEFAULT_OUTPUT_RATE
    }

    /** Build (but do not start) a game audio track at [rate]. */
    fun buildTrack(rate: Int): AudioTrack {
        val minBuf = AudioTrack.getMinBufferSize(
            rate, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT
        )
        return AudioTrack.Builder()
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
    }
}
