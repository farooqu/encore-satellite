package com.encore.satellite

import android.content.Context
import android.hardware.Sensor
import android.hardware.SensorEvent
import android.hardware.SensorEventListener
import android.hardware.SensorManager
import android.os.Handler
import android.os.HandlerThread
import android.os.SystemClock
import com.encore.engine.mobile.LightHost
import org.json.JSONObject

/** Low-rate, service-owned ALS. No audio focus, wake lock or dangerous permission. */
class LightSensorHost(context: Context) : LightHost, SensorEventListener, AutoCloseable {
    private val manager = context.getSystemService(SensorManager::class.java)
    private val sensor = manager?.getDefaultSensor(Sensor.TYPE_LIGHT)
    private val thread = if (sensor != null) HandlerThread("Encore illuminance").apply { start() } else null
    private val handler = thread?.let { Handler(it.looper) }
    @Volatile private var closed = false
    private var lux = 0.0
    private var timestamp = 0L
    private var problem = "Awaiting light sample"
    private val registration = Runnable { register() }

    init { handler?.post(registration) }

    override fun supported() = sensor != null

    // SensorManager registers callbacks on a dedicated looper; JNI reads only
    // this short synchronized snapshot, never the main/audio/recorder threads.
    @Synchronized override fun sample(): String {
        val age = (SystemClock.elapsedRealtimeNanos() - timestamp) / 1_000_000
        if (!closed && (timestamp == 0L || age > 20_000)) {
            // TYPE_LIGHT is commonly on-change. Re-register to request a new
            // initial sample rather than inventing fresh timestamps for old data.
            // At most one retry queues behind a slow HAL operation.
            handler?.removeCallbacks(registration)
            handler?.post(registration)
        }
        return JSONObject().apply {
            if (closed || problem.isNotEmpty()) put("problem", if (closed) "Light sensor stopped" else problem)
            else { put("lux", lux); put("age_ms", age) }
        }.toString()
    }

    private fun register() {
        if (closed || sensor == null) return
        // Binder/HAL calls stay on the sensor looper, outside the snapshot lock.
        runCatching {
            manager.unregisterListener(this)
            check(manager.registerListener(this, sensor, SensorManager.SENSOR_DELAY_NORMAL, handler)) { "Light sensor registration failed" }
        }.onFailure {
            synchronized(this) {
                timestamp = 0
                problem = if (it is SecurityException) "Light sensor permission denied" else "Light sensor registration failed"
            }
        }
    }

    @Synchronized override fun onSensorChanged(event: SensorEvent) {
        if (closed || event.sensor != sensor) return
        val value = event.values.firstOrNull()?.toDouble()
        if (value == null || !value.isFinite() || value < 0 || event.timestamp <= 0) {
            timestamp = 0
            problem = "Invalid light sample"
        } else {
            lux = value
            timestamp = event.timestamp
            problem = ""
        }
    }

    override fun onAccuracyChanged(sensor: Sensor?, accuracy: Int) {}

    @Synchronized override fun close() {
        if (closed) return
        closed = true
        timestamp = 0
        // Queued behind any in-flight registration; late callbacks are fenced
        // immediately by closed. Audio shutdown never waits for a HAL call.
        handler?.removeCallbacks(registration)
        handler?.post { manager?.unregisterListener(this); thread?.quitSafely() }
    }
}
