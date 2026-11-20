package com.encore.satellite

import android.content.Context
import android.hardware.Sensor
import android.hardware.SensorManager
import android.os.SystemClock
import androidx.test.core.app.ApplicationProvider
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.SensorEventBuilder
import org.robolectric.shadows.ShadowLooper
import org.robolectric.shadows.ShadowSensor

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class LightSensorTest {
    @Test fun noLightSensorCreatesNoHostOrLooper() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val manager = shadowOf(context.getSystemService(SensorManager::class.java))
        manager.addSensor(ShadowSensor.newInstance(Sensor.TYPE_PROXIMITY))
        val existingLoopers = ShadowLooper.getAllLoopers().toSet()
        assertNull(LightSensorHost.create(context))
        assertEquals(existingLoopers, ShadowLooper.getAllLoopers().toSet())
    }

    @Test fun registrationFailureIsUnknownNotAbsentAndCanRecover() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val manager = shadowOf(context.getSystemService(SensorManager::class.java))
        val sensor = ShadowSensor.newInstance(Sensor.TYPE_LIGHT)
        manager.addSensor(sensor)
        manager.setForceListenersToFail(true)
        val existingLoopers = ShadowLooper.getAllLoopers().toSet()
        checkNotNull(LightSensorHost.create(context)).use { host ->
            // Other test classes can leave quit loopers in Robolectric's registry.
            val sensorLooper = ShadowLooper.getAllLoopers().single { it !in existingLoopers }
            shadowOf(sensorLooper).idle()
            assertEquals("Light sensor registration failed", JSONObject(host.sample()).getString("problem"))
            manager.setForceListenersToFail(false)
            host.sample()
            shadowOf(sensorLooper).idle()
            SystemClock.sleep(10)
            manager.sendSensorEventToListeners(SensorEventBuilder.newBuilder().setSensor(sensor)
                .setTimestamp(SystemClock.elapsedRealtimeNanos()).setValues(floatArrayOf(71f)).build())
            assertEquals(71.0, JSONObject(host.sample()).getDouble("lux"), 0.0)
            host.close()
            sensorLooper.thread.join(2_000)
            assertFalse(sensorLooper.thread.isAlive)
            assertFalse(manager.hasListener(host))
        }
    }

    @Test fun luxUsesEventAgeAndRejectsBadAndPostShutdownSamples() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val sensor = ShadowSensor.newInstance(Sensor.TYPE_LIGHT)
        shadowOf(context.getSystemService(SensorManager::class.java)).addSensor(sensor)
        checkNotNull(LightSensorHost.create(context)).use { host ->
            assertEquals("Awaiting light sample", JSONObject(host.sample()).getString("problem"))
            SystemClock.sleep(10)
            val event = SensorEventBuilder.newBuilder().setSensor(sensor)
                .setTimestamp(SystemClock.elapsedRealtimeNanos()).setValues(floatArrayOf(213.5f)).build()
            host.onSensorChanged(event)
            assertEquals(213.5, JSONObject(host.sample()).getDouble("lux"), 0.0)
            SystemClock.sleep(1_234)
            assertTrue(JSONObject(host.sample()).getLong("age_ms") >= 1_234)
            event.values[0] = Float.NaN
            host.onSensorChanged(event)
            val invalid = JSONObject(host.sample())
            assertEquals("Invalid light sample", invalid.getString("problem"))
            assertFalse(invalid.has("lux"))
            event.values[0] = 0f
            host.onSensorChanged(event)
            assertEquals(0.0, JSONObject(host.sample()).getDouble("lux"), 0.0)
            host.close()
            host.onSensorChanged(event)
            val stopped = JSONObject(host.sample())
            assertEquals("Light sensor stopped", stopped.getString("problem"))
            assertFalse(stopped.has("lux"))
        }
    }
}
