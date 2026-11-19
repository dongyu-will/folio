package dev.mygo

import android.app.Activity
import android.app.Application
import android.content.Context
import android.content.res.Configuration
import android.graphics.drawable.ColorDrawable
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Choreographer
import android.view.View
import android.view.ViewGroup
import java.lang.ref.WeakReference
import java.util.Locale

/**
 * Keeps an opaque shell-colored cover above HostImpl.root during cold start and
 * warm resume so SurfaceView's black hole-punch never flashes while EGL/GLES3
 * compiles shaders and latches the first buffer.
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

    private const val READY_EXTRA_FRAMES = 3
    private const val FADE_DURATION_MS = 120L
    private const val COLD_TIMEOUT_MS = 4500L
    private const val RESUME_TIMEOUT_MS = 650L

    private enum class State {
        IDLE,
        COLD_WAIT,
        STOPPED,
        RESUME_PENDING_EMIT,
        RESUME_WAIT,
        COUNTDOWN,
        FADING,
    }

    private val main = Handler(Looper.getMainLooper())
    private var appContext: Context? = null
    private var registered = false
    private var cachedAppearance = ""
    private var currentActivity: WeakReference<Activity>? = null
    private var coverView: View? = null
    private var surfaceLost = false
    private var state = State.IDLE
    private var generation = 0

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
            if (!surfaceLost) return
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
                armFailSafe(gen, RESUME_TIMEOUT_MS)
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

        override fun onActivityPaused(activity: Activity) = Unit

        override fun onActivityStopped(activity: Activity) {
            if (activity !is MyGoActivity) return
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
            coverView?.let { view ->
                if (view.context === activity) {
                    removeCoverView(view)
                }
            }
            if (currentActivity?.get() === activity) {
                cancelPending()
                generation++
                currentActivity = null
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
            if (state == State.COLD_WAIT || state == State.RESUME_WAIT) {
                startFrameCountdown(generation)
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

    private fun attachCover(activity: Activity) {
        val content = activity.findViewById<ViewGroup>(android.R.id.content) ?: return
        val color = resolveColor(activity)
        val existing = coverView
        if (existing != null && existing.parent === content) {
            existing.animate().cancel()
            existing.setBackgroundColor(color)
            existing.alpha = 1f
            existing.visibility = View.VISIBLE
            existing.isClickable = true
            existing.bringToFront()
            return
        }
        if (existing != null) {
            removeCoverView(existing)
        }
        val view = View(activity).apply {
            setBackgroundColor(color)
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
                startFadeOut(gen)
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
                    startFadeOut(gen)
                } else {
                    choreographer.postFrameCallback(this)
                }
            }
        }
        activeFrameCallback = cb
        choreographer.postFrameCallback(cb)
    }

    private fun startFadeOut(gen: Int) {
        if (gen != generation) return
        cancelPending()
        val view = coverView ?: run {
            state = State.IDLE
            return
        }
        if (!view.isAttachedToWindow) {
            removeCoverView(view)
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
