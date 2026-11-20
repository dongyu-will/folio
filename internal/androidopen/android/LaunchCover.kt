package dev.mygo

import android.app.Activity
import android.app.Application
import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import android.graphics.drawable.ColorDrawable
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Choreographer
import android.view.PixelCopy
import android.view.SurfaceView
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import androidx.core.view.children
import java.lang.ref.WeakReference
import java.util.Locale

/**
 * Covers HostImpl.root until the first frame is ready. On warm resume the
 * cover shows the last workspace frame while SurfaceView acquires a buffer.
 *
 * Attached exclusively to android.R.id.content as a top sibling of HostImpl.root
 * (never inside HostImpl.root, where Navigation.arrange() hides non-top children).
 */
internal object LaunchCover {
    private const val PLUGIN_ID = "mgnotedownloads"
    private const val EVENT_SURFACE_RESUMED = "surfaceResumed"
    private const val PREFS_NAME = "folio_prefs"
    private const val KEY_APPEARANCE = "appearance"

    private const val COLOR_LIGHT = 0xFFF5F5F7.toInt()
    private const val COLOR_DARK = 0xFF0E0F12.toInt()
    private const val RESUME_FRAME_MAX_EDGE = 2048

    private const val READY_EXTRA_FRAMES = 3
    private const val FADE_DURATION_MS = 120L
    private const val COLD_TIMEOUT_MS = 4500L
    private const val RESUME_TIMEOUT_MS = 650L
    private const val RESUME_FRAME_TIMEOUT_MS = 1500L

    private enum class State {
        IDLE,
        COLD_WAIT,
        STOPPED,
        RESUME_PENDING_EMIT,
        RESUME_WAIT,
        RESUME_BUFFER,
        COUNTDOWN,
        FADING,
    }

    private val main = Handler(Looper.getMainLooper())
    private var appContext: Context? = null
    private var registered = false
    private var cachedAppearance = ""
    private var currentActivity: WeakReference<Activity>? = null
    private var resumedActivity: WeakReference<Activity>? = null
    private var onActivityResumedListener: ((Activity) -> Unit)? = null
    private var coverView: ImageView? = null
    private var resumeFrame: Bitmap? = null
    private var captureSequence = 0
    private var surfaceLost = false
    private var state = State.IDLE
    private var generation = 0

    @Volatile
    var isResumed: Boolean = false
        private set

    private var activeFrameCallback: Choreographer.FrameCallback? = null
    private var failSafeRunnable: Runnable? = null
    private var pendingEmitRunnable: Runnable? = null

    private val lifecycle = object : Application.ActivityLifecycleCallbacks {
        override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) {
            if (activity !is MyGoActivity) return
            // Fires on Activity recreation (or first Activity after headless start);
            // HostImpl.attach() calls setContentView(root) later in onCreate, so
            // set the window background now and attach the sibling cover in onStart.
            currentActivity = WeakReference(activity)
            clearResumeFrame()
            detachCoverIfDifferent(activity)
            cancelPending()
            generation++
            surfaceLost = true
            state = State.STOPPED
            applyWindowBackground(activity)
        }

        override fun onActivityStarted(activity: Activity) {
            if (activity !is MyGoActivity) return
            currentActivity = WeakReference(activity)
            if (surfaceLost) {
                applyWindowBackground(activity)
                attachCover(activity)
            }
        }

        override fun onActivityResumed(activity: Activity) {
            if (activity !is MyGoActivity) return
            currentActivity = WeakReference(activity)
            resumedActivity = WeakReference(activity)
            isResumed = true
            if (onActivityResumedListener != null) {
                main.post {
                    if (isResumed && resumedActivity?.get() === activity &&
                        !activity.isFinishing && !activity.isDestroyed
                    ) {
                        onActivityResumedListener?.invoke(activity)
                    }
                }
            }
            if (!surfaceLost) {
                clearResumeFrame()
                return
            }
            surfaceLost = false
            applyWindowBackground(activity)
            attachCover(activity)
            cancelPending()
            val gen = ++generation
            if (!HostImpl.drew) {
                state = State.COLD_WAIT
                armFailSafe(gen, COLD_TIMEOUT_MS)
                startColdPoll(gen)
            } else {
                state = State.RESUME_PENDING_EMIT
                val timeout = if (resumeFrame != null) RESUME_FRAME_TIMEOUT_MS else RESUME_TIMEOUT_MS
                armFailSafe(gen, timeout)
                val emit = Runnable {
                    pendingEmitRunnable = null
                    if (gen != generation || state != State.RESUME_PENDING_EMIT) return@Runnable
                    state = State.RESUME_WAIT
                    PluginHost.emit(PLUGIN_ID, EVENT_SURFACE_RESUMED)
                }
                pendingEmitRunnable = emit
                main.post(emit)
            }
        }

        override fun onActivityPaused(activity: Activity) {
            if (activity !is MyGoActivity) return
            if (resumedActivity?.get() === activity) {
                resumedActivity = null
                isResumed = false
            }
            if (currentActivity?.get() === activity && !activity.isFinishing && HostImpl.drew) {
                captureResumeFrame(activity)
            }
        }

        override fun onActivityStopped(activity: Activity) {
            if (activity !is MyGoActivity) return
            if (resumedActivity?.get() === activity) {
                resumedActivity = null
                isResumed = false
            }
            if (currentActivity?.get() !== activity) return
            cancelPending()
            generation++
            surfaceLost = true
            state = State.STOPPED
            if (!activity.isFinishing) {
                applyWindowBackground(activity)
                attachCover(activity)
            }
        }

        override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit

        override fun onActivityDestroyed(activity: Activity) {
            if (activity !is MyGoActivity) return
            if (resumedActivity?.get() === activity) {
                resumedActivity = null
                isResumed = false
            }
            coverView?.let { view ->
                if (view.context === activity) {
                    removeCoverView(view)
                }
            }
            if (currentActivity?.get() === activity) {
                cancelPending()
                generation++
                currentActivity = null
                clearResumeFrame()
                state = State.IDLE
            }
        }
    }

    fun install(context: Context, activity: Activity?) {
        runOnMain {
            val app = context.applicationContext
            appContext = app
            cachedAppearance = normalizeAppearance(
                app.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                    .getString(KEY_APPEARANCE, "")
            )
            if (!registered && app is Application) {
                app.registerActivityLifecycleCallbacks(lifecycle)
                registered = true
            }
            if (activity is MyGoActivity) {
                currentActivity = WeakReference(activity)
                applyWindowBackground(activity)
                attachCover(activity)
                cancelPending()
                surfaceLost = false
                val gen = ++generation
                state = State.COLD_WAIT
                armFailSafe(gen, COLD_TIMEOUT_MS)
                if (HostImpl.drew) {
                    startFrameCountdown(gen)
                } else {
                    startColdPoll(gen)
                }
            }
        }
    }

    fun currentResumedActivity(): Activity? {
        if (!isResumed) return null
        val activity = resumedActivity?.get() ?: return null
        if (activity.isFinishing || activity.isDestroyed) return null
        return activity
    }

    fun setOnActivityResumedListener(listener: ((Activity) -> Unit)?) {
        runOnMain {
            onActivityResumedListener = listener
        }
    }

    fun setAppearance(raw: String?) {
        runOnMain {
            val normalized = normalizeAppearance(raw)
            cachedAppearance = normalized
            val ctx = appContext ?: PluginHost.context
            ctx.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                .edit()
                .putString(KEY_APPEARANCE, normalized)
                .apply()
            val activity = currentActivity?.get() ?: PluginHost.activity
            val color = resolveColor(activity ?: ctx)
            activity?.window?.setBackgroundDrawable(ColorDrawable(color))
            coverView?.setBackgroundColor(color)
        }
    }

    fun onFrameReady() {
        runOnMain {
            when (state) {
                State.COLD_WAIT -> startFrameCountdown(generation)
                State.RESUME_WAIT -> waitForResumeBuffer(generation)
                else -> Unit
            }
        }
    }

    private fun runOnMain(block: () -> Unit) {
        if (Looper.myLooper() == Looper.getMainLooper()) {
            block()
        } else {
            main.post(block)
        }
    }

    private fun normalizeAppearance(raw: String?): String = when (raw?.trim()?.lowercase(Locale.ROOT)) {
        "light" -> "light"
        "dark" -> "dark"
        else -> ""
    }

    private fun resolveColor(context: Context): Int = when (cachedAppearance) {
        "light" -> COLOR_LIGHT
        "dark" -> COLOR_DARK
        else -> {
            val night = (context.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) ==
                Configuration.UI_MODE_NIGHT_YES
            if (night) COLOR_DARK else COLOR_LIGHT
        }
    }

    private fun applyWindowBackground(activity: Activity) {
        activity.window?.setBackgroundDrawable(ColorDrawable(resolveColor(activity)))
    }

    private fun workspaceSurface(): SurfaceView? {
        val workspace = (HostImpl.front() as? SurfaceWindow)?.view as? ViewGroup ?: return null
        return workspace.children.filterIsInstance<SurfaceView>().firstOrNull()
    }

    private fun captureResumeFrame(activity: Activity) {
        val request = ++captureSequence
        val surface = workspaceSurface() ?: return
        val width = surface.width
        val height = surface.height
        if (width <= 0 || height <= 0 || !surface.holder.surface.isValid) return

        // Keep a bounded, in-memory copy of Folio's last rendered buffer.
        val scale = minOf(1f, RESUME_FRAME_MAX_EDGE.toFloat() / maxOf(width, height))
        val frame = Bitmap.createBitmap(
            (width * scale).toInt().coerceAtLeast(1),
            (height * scale).toInt().coerceAtLeast(1),
            Bitmap.Config.ARGB_8888,
        )
        try {
            PixelCopy.request(surface, frame, { result ->
                if (result == PixelCopy.SUCCESS && currentActivity?.get() === activity &&
                    request == captureSequence
                ) {
                    resumeFrame = frame
                    if (state != State.FADING) coverView?.setImageBitmap(frame)
                }
            }, main)
        } catch (_: IllegalArgumentException) {
            // The surface can disappear as Android hides the window.
        }
    }

    private fun clearResumeFrame() {
        captureSequence++
        resumeFrame = null
    }

    private fun attachCover(activity: Activity) {
        val content = activity.findViewById<ViewGroup>(android.R.id.content) ?: return
        val color = resolveColor(activity)
        val existing = coverView
        if (existing != null && existing.parent === content) {
            existing.animate().cancel()
            existing.setBackgroundColor(color)
            existing.setImageBitmap(resumeFrame)
            existing.alpha = 1f
            existing.visibility = View.VISIBLE
            existing.isClickable = true
            existing.bringToFront()
            return
        }
        if (existing != null) {
            removeCoverView(existing)
        }
        val view = ImageView(activity).apply {
            setBackgroundColor(color)
            scaleType = ImageView.ScaleType.FIT_CENTER
            setImageBitmap(resumeFrame)
            alpha = 1f
            isClickable = true
            isFocusable = false
            isFocusableInTouchMode = false
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }
        content.addView(
            view,
            ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
        )
        view.bringToFront()
        coverView = view
    }

    private fun detachCoverIfDifferent(activity: Activity) {
        val existing = coverView ?: return
        if (existing.context !== activity) {
            removeCoverView(existing)
        }
    }

    private fun removeCoverView(view: View) {
        view.animate().cancel()
        (view.parent as? ViewGroup)?.removeView(view)
        if (coverView === view) {
            coverView = null
        }
    }

    private fun armFailSafe(gen: Int, timeoutMs: Long) {
        failSafeRunnable?.let { main.removeCallbacks(it) }
        val runnable = Runnable {
            failSafeRunnable = null
            if (gen == generation && state != State.IDLE && state != State.FADING && state != State.STOPPED) {
                dismissCover(gen)
            }
        }
        failSafeRunnable = runnable
        main.postDelayed(runnable, timeoutMs)
    }

    private fun startColdPoll(gen: Int) {
        cancelFrameCallback()
        val choreographer = Choreographer.getInstance()
        val cb = object : Choreographer.FrameCallback {
            override fun doFrame(frameTimeNanos: Long) {
                if (gen != generation || state != State.COLD_WAIT) return
                if (HostImpl.drew) {
                    activeFrameCallback = null
                    startFrameCountdown(gen)
                } else {
                    choreographer.postFrameCallback(this)
                }
            }
        }
        activeFrameCallback = cb
        choreographer.postFrameCallback(cb)
    }

    private fun waitForResumeBuffer(gen: Int) {
        // Go's paint notification can precede the new surface's first buffer.
        // Keep the cover up until PixelCopy can read from that surface.
        state = State.RESUME_BUFFER
        val probe = Bitmap.createBitmap(1, 1, Bitmap.Config.ARGB_8888)
        val choreographer = Choreographer.getInstance()
        val cb = object : Choreographer.FrameCallback {
            override fun doFrame(frameTimeNanos: Long) {
                if (gen != generation || state != State.RESUME_BUFFER) return
                val surface = workspaceSurface()
                if (surface != null && surface.holder.surface.isValid) {
                    try {
                        PixelCopy.request(surface, probe, { result ->
                            if (gen == generation && state == State.RESUME_BUFFER) {
                                if (result == PixelCopy.SUCCESS) {
                                    startFrameCountdown(gen)
                                } else {
                                    choreographer.postFrameCallback(this)
                                }
                            }
                        }, main)
                        return
                    } catch (_: IllegalArgumentException) {
                        // Android detached this surface before the probe could run.
                    }
                }
                choreographer.postFrameCallback(this)
            }
        }
        activeFrameCallback = cb
        choreographer.postFrameCallback(cb)
    }

    private fun startFrameCountdown(gen: Int) {
        if (gen != generation) return
        if (state == State.COUNTDOWN || state == State.FADING || state == State.IDLE) return
        cancelFrameCallback()
        state = State.COUNTDOWN
        var remaining = READY_EXTRA_FRAMES
        val choreographer = Choreographer.getInstance()
        val cb = object : Choreographer.FrameCallback {
            override fun doFrame(frameTimeNanos: Long) {
                if (gen != generation || state != State.COUNTDOWN) return
                remaining--
                if (remaining <= 0) {
                    activeFrameCallback = null
                    dismissCover(gen)
                } else {
                    choreographer.postFrameCallback(this)
                }
            }
        }
        activeFrameCallback = cb
        choreographer.postFrameCallback(cb)
    }

    private fun dismissCover(gen: Int) {
        if (gen != generation) return
        cancelPending()
        val view = coverView
        // The saved frame and the live surface show the same workspace, so a
        // single swap avoids fading through SurfaceView's transparent region.
        if (view == null || !view.isAttachedToWindow || view.drawable != null) {
            if (view != null) removeCoverView(view)
            if (isResumed) clearResumeFrame()
            state = State.IDLE
            return
        }
        state = State.FADING
        view.isClickable = false
        view.animate().cancel()
        view.animate()
            .alpha(0f)
            .setDuration(FADE_DURATION_MS)
            .withEndAction {
                if (gen == generation && state == State.FADING && coverView === view) {
                    removeCoverView(view)
                    if (isResumed) clearResumeFrame()
                    state = State.IDLE
                }
            }
            .start()
    }

    private fun cancelFrameCallback() {
        activeFrameCallback?.let { Choreographer.getInstance().removeFrameCallback(it) }
        activeFrameCallback = null
    }

    private fun cancelPending() {
        cancelFrameCallback()
        failSafeRunnable?.let { main.removeCallbacks(it) }
        failSafeRunnable = null
        pendingEmitRunnable?.let { main.removeCallbacks(it) }
        pendingEmitRunnable = null
    }
}
