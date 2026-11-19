package dev.mygo

import android.app.Activity
import android.os.Build
import android.view.MotionEvent
import android.view.View
import android.view.Window
import org.json.JSONArray
import org.json.JSONObject

/** Forwards Android stylus samples with their pressure to the Go canvas. */
internal class StylusBridge {
    private data class Zone(val name: String, val x: Double, val y: Double, val w: Double, val h: Double) {
        fun contains(x: Double, y: Double) = x >= this.x && x < this.x + w && y >= this.y && y < this.y + h
    }

    @Volatile
    private var zones = emptyList<Zone>()
    private var boundActivity: Activity? = null
    private var windowCallback: PenCallback? = null

    fun update(input: JSONArray) {
        zones = List(input.length()) { i ->
            val zone = input.getJSONObject(i)
            Zone(zone.getString("name"), zone.getDouble("x"), zone.getDouble("y"),
                zone.getDouble("w"), zone.getDouble("h"))
        }
        val activity = PluginHost.activity ?: return
        val window = activity.window
        val current = window.callback
        if (activity !== boundActivity || current !== windowCallback) {
            val surface = activity.findViewById<View>(android.R.id.content) ?: return
            val callback = PenCallback(current, surface)
            boundActivity = activity
            windowCallback = callback
            window.callback = callback
        }
    }

    private inner class PenCallback(private val next: Window.Callback, private val content: View) : Window.Callback by next {
        private var pointer = -1
        private var zone = ""
        private var eraser = false
        private val location = IntArray(2)

        override fun dispatchTouchEvent(event: MotionEvent): Boolean {
            val activeZones = zones
            if (pointer < 0 && activeZones.isEmpty()) {
                return next.dispatchTouchEvent(event)
            }
            val action = event.actionMasked
            val index = event.actionIndex
            val density = content.resources.displayMetrics.density.toDouble()
            content.getLocationInWindow(location)
            val ox = location[0]
            val oy = location[1]
            val isDown = action == MotionEvent.ACTION_DOWN || action == MotionEvent.ACTION_POINTER_DOWN
            if (pointer < 0 && isDown) {
                val tool = event.getToolType(index)
                val stylus = tool == MotionEvent.TOOL_TYPE_STYLUS || tool == MotionEvent.TOOL_TYPE_ERASER
                if (stylus) {
                    val x = (event.getX(index) - ox) / density
                    val y = (event.getY(index) - oy) / density
                    val found = activeZones.firstOrNull { it.contains(x, y) }
                    if (found != null) {
                        // Cancel any prior palm touch so resting a hand before pen-down never pans or clicks cards.
                        if (action == MotionEvent.ACTION_POINTER_DOWN) {
                            val cancel = MotionEvent.obtain(event)
                            cancel.action = MotionEvent.ACTION_CANCEL
                            next.dispatchTouchEvent(cancel)
                            cancel.recycle()
                        }
                        content.requestUnbufferedDispatch(event)
                        pointer = event.getPointerId(index)
                        zone = found.name
                        val btn = event.buttonState
                        eraser = tool == MotionEvent.TOOL_TYPE_ERASER ||
                            (btn and (MotionEvent.BUTTON_STYLUS_PRIMARY or MotionEvent.BUTTON_STYLUS_SECONDARY)) != 0
                    }
                }
            }
            if (pointer < 0) return next.dispatchTouchEvent(event)

            val i = event.findPointerIndex(pointer)
            val canceledPointerUp = Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                action == MotionEvent.ACTION_POINTER_UP && index == i &&
                (event.flags and MotionEvent.FLAG_CANCELED) != 0
            val phase = when {
                action == MotionEvent.ACTION_CANCEL || i < 0 || canceledPointerUp -> "cancel"
                isDown && index == i -> "down"
                action == MotionEvent.ACTION_MOVE -> "move"
                (action == MotionEvent.ACTION_UP || action == MotionEvent.ACTION_POINTER_UP) && index == i -> "up"
                else -> return true
            }
            val samples = JSONArray()
            fun add(x: Float, y: Float, pressure: Float) {
                if (x.isFinite() && y.isFinite() && pressure.isFinite()) {
                    samples.put(JSONArray().put((x - ox) / density).put((y - oy) / density).put(pressure.toDouble()))
                }
            }
            if (phase == "move") {
                for (h in 0 until event.historySize) {
                    add(event.getHistoricalX(i, h), event.getHistoricalY(i, h), event.getHistoricalPressure(i, h))
                }
            }
            if (i >= 0) add(event.getX(i), event.getY(i), event.getPressure(i))
            PluginHost.emit("mgnotedownloads", "stylus", JSONObject()
                .put("zone", zone).put("phase", phase).put("eraser", eraser).put("samples", samples))
            if (phase == "up" || phase == "cancel") {
                pointer = -1
                eraser = false
            }
            return true
        }
    }
}
