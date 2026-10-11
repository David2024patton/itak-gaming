package net.itak.itak_gaming

import android.content.Context
import android.opengl.GLES20
import android.opengl.GLSurfaceView
import android.util.Log
import mobile.Mobile
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.nio.FloatBuffer
import javax.microedition.khronos.egl.EGLConfig
import javax.microedition.khronos.opengles.GL10

/** Called once the GL surface exists and frames can be pulled. */
interface TextureListener {
    fun onSurfaceReady()
}

/** Picture filter shared between the pause menu and the renderer. */
object FilterSettings {
    val NAMES = arrayOf("Sharp", "Smooth", "Pixel Perfect")

    /** 0 = Sharp (sharp bilinear), 1 = Smooth (linear), 2 = Pixel (nearest). */
    @Volatile var mode: Int = 0

    val DISPLAY_NAMES = arrayOf("Smooth 60", "Steady 30")

    /**
     * 0 = draw every emulated frame (60 draws/s), 1 = draw every other
     * frame (an even 30 draws/s). Emulation and audio always run at the
     * core's full rate either way; only the draw cadence changes.
     */
    @Volatile var displayMode: Int = 0
}

/**
 * OpenGL ES 2.0 view for emulator frames.
 *
 * Each frame is a small texture upload (256x224 for SNES) plus one quad;
 * the GPU does the upscale to the TV. This replaces a software Canvas
 * path that cost 23-76 ms per frame on the Chromecast's CPU.
 */
class EmuGLView(context: Context) : GLSurfaceView(context) {
    private val emuRenderer: EmuRenderer

    init {
        setEGLContextClientVersion(2)
        setEGLConfigChooser(8, 8, 8, 8, 0, 0)
        emuRenderer = EmuRenderer(context as? TextureListener)
        setRenderer(emuRenderer)
        renderMode = RENDERMODE_WHEN_DIRTY
    }
}

class EmuRenderer(private val listener: TextureListener?) : GLSurfaceView.Renderer {
    private var program = 0
    private var textureId = 0
    private var posHandle = 0
    private var texHandle = 0
    private var samplerHandle = 0
    private var texSizeHandle = 0
    private var modeHandle = 0
    private var widthHandle = 0
    private var lastFilterMode = -1
    private var viewW = 0
    private var viewH = 0
    private var texW = 0
    private var texH = 0
    private var draws = 0L
    private var tDraw = 0L
    private var ready = false

    private val quadBuffer: FloatBuffer = ByteBuffer
        .allocateDirect(QUAD.size * 4)
        .order(ByteOrder.nativeOrder())
        .asFloatBuffer()
        .apply { put(QUAD); position(0) }

    override fun onSurfaceCreated(gl: GL10?, config: EGLConfig?) {
        GLES20.glClearColor(0f, 0f, 0f, 1f)
        program = buildProgram()
        posHandle = GLES20.glGetAttribLocation(program, "aPos")
        texHandle = GLES20.glGetAttribLocation(program, "aTex")
        samplerHandle = GLES20.glGetUniformLocation(program, "uTex")
        texSizeHandle = GLES20.glGetUniformLocation(program, "uTexSize")
        modeHandle = GLES20.glGetUniformLocation(program, "uMode")
        widthHandle = GLES20.glGetUniformLocation(program, "uWidth")

        val ids = IntArray(1)
        GLES20.glGenTextures(1, ids, 0)
        textureId = ids[0]
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, textureId)
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MIN_FILTER, GLES20.GL_NEAREST)
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MAG_FILTER, GLES20.GL_LINEAR)
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_WRAP_S, GLES20.GL_CLAMP_TO_EDGE)
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_WRAP_T, GLES20.GL_CLAMP_TO_EDGE)

        ready = true
        listener?.onSurfaceReady()
        Log.i(TAG, "surface created")
    }

    override fun onSurfaceChanged(gl: GL10?, width: Int, height: Int) {
        viewW = width
        viewH = height
        Log.i(TAG, "surface changed ${width}x${height}")
    }

    override fun onDrawFrame(gl: GL10?) {
        if (!ready) return
        val s0 = System.nanoTime()

        val w = Mobile.frameWidth().toInt()
        val h = Mobile.frameHeight().toInt()
        GLES20.glClear(GLES20.GL_COLOR_BUFFER_BIT)
        if (w <= 0 || h <= 0) return
        val rgba = Mobile.frameRGBA() ?: return
        if (rgba.size < w * h * 4) return

        GLES20.glUseProgram(program)
        GLES20.glActiveTexture(GLES20.GL_TEXTURE0)
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, textureId)
        GLES20.glUniform1i(samplerHandle, 0)

        // Picture filter: Sharp = sharp-bilinear remap in the shader,
        // Smooth = plain linear, Pixel = nearest texel sampling.
        val mode = FilterSettings.mode
        GLES20.glUniform2f(texSizeHandle, w.toFloat(), h.toFloat())
        GLES20.glUniform1f(modeHandle, mode.toFloat())
        GLES20.glUniform1f(widthHandle, 0.35f)
        if (mode != lastFilterMode) {
            lastFilterMode = mode
            val mag = if (mode == 2) GLES20.GL_NEAREST else GLES20.GL_LINEAR
            GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MAG_FILTER, mag)
        }

        val buf = ByteBuffer.wrap(rgba)
        if (w != texW || h != texH) {
            GLES20.glTexImage2D(
                GLES20.GL_TEXTURE_2D, 0, GLES20.GL_RGBA, w, h, 0,
                GLES20.GL_RGBA, GLES20.GL_UNSIGNED_BYTE, buf
            )
            texW = w; texH = h
        } else {
            GLES20.glTexSubImage2D(
                GLES20.GL_TEXTURE_2D, 0, 0, 0, w, h,
                GLES20.GL_RGBA, GLES20.GL_UNSIGNED_BYTE, buf
            )
        }

        // Aspect-correct viewport (pillarbox), centered.
        val aspect = Mobile.aspectRatio().toDouble()
        var vw = viewW
        var vh = (viewW / aspect).toInt()
        if (vh > viewH) {
            vh = viewH
            vw = (viewH * aspect).toInt()
        }
        GLES20.glViewport((viewW - vw) / 2, (viewH - vh) / 2, vw, vh)

        quadBuffer.position(0)
        GLES20.glEnableVertexAttribArray(posHandle)
        GLES20.glVertexAttribPointer(posHandle, 2, GLES20.GL_FLOAT, false, 16, quadBuffer)
        quadBuffer.position(2)
        GLES20.glEnableVertexAttribArray(texHandle)
        GLES20.glVertexAttribPointer(texHandle, 2, GLES20.GL_FLOAT, false, 16, quadBuffer)
        GLES20.glDrawArrays(GLES20.GL_TRIANGLE_STRIP, 0, 4)
        GLES20.glDisableVertexAttribArray(posHandle)
        GLES20.glDisableVertexAttribArray(texHandle)

        tDraw += (System.nanoTime() - s0) / 1_000_000
        draws++
        if (draws % 600 == 0L) {
            Log.i(TAG, "gl draws=$draws avg draw ms=${tDraw / 600}")
            tDraw = 0
        }
    }

    private fun buildProgram(): Int {
        val vs = """
            attribute vec4 aPos;
            attribute vec2 aTex;
            varying vec2 vTex;
            void main() {
                gl_Position = aPos;
                vTex = aTex;
            }
        """.trimIndent()
        val fs = """
            precision mediump float;
            varying vec2 vTex;
            uniform sampler2D uTex;
            uniform vec2 uTexSize;
            uniform float uMode;
            uniform float uWidth;
            void main() {
                vec2 uv = vTex;
                if (uMode < 0.5) {
                    // Sharp bilinear: keep the sample pinned near the
                    // texel center (crisp pixels) but blend in a narrow
                    // band at texel borders (no shimmer).
                    vec2 texel = vTex * uTexSize;
                    vec2 center = floor(texel) + 0.5;
                    vec2 f = texel - center;
                    vec2 a = abs(f);
                    vec2 h = sign(f) * 0.5 * clamp((a - (0.5 - uWidth)) / uWidth, 0.0, 1.0);
                    uv = (center + h) / uTexSize;
                }
                gl_FragColor = texture2D(uTex, uv);
            }
        """.trimIndent()
        val v = compile(GLES20.GL_VERTEX_SHADER, vs)
        val f = compile(GLES20.GL_FRAGMENT_SHADER, fs)
        val p = GLES20.glCreateProgram()
        GLES20.glAttachShader(p, v)
        GLES20.glAttachShader(p, f)
        GLES20.glLinkProgram(p)
        return p
    }

    private fun compile(type: Int, src: String): Int {
        val id = GLES20.glCreateShader(type)
        GLES20.glShaderSource(id, src)
        GLES20.glCompileShader(id)
        return id
    }

    companion object {
        private const val TAG = "iTaKGL"

        // x, y, u, v per vertex (triangle strip). v flipped: GL textures
        // are bottom-up, emulator frames are top-down.
        private val QUAD = floatArrayOf(
            -1f, -1f, 0f, 1f,
            1f, -1f, 1f, 1f,
            -1f, 1f, 0f, 0f,
            1f, 1f, 1f, 0f
        )
    }
}
