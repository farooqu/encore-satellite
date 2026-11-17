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
    @Test fun absentHardwareDoesNotClaimCapability() {
        LightSensorHost(ApplicationProvider.getApplicationContext()).use { host ->
            assertFalse(host.supported())
            assertTrue(JSONObject(host.sample()).has("problem"))
        }
    }

    @Test fun registrationFailureIsUnknownNotAbsentAndCanRecover() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val manager = shadowOf(context.getSystemService(SensorManager::class.java))
        val sensor = ShadowSensor.newInstance(Sensor.TYPE_LIGHT)
        manager.addSensor(sensor)
        manager.setForceListenersToFail(true)
        val existingLoopers = ShadowLooper.getAllLoopers().toSet()
        LightSensorHost(context).use { host ->
            // Other test classes can leave quit loopers in Robolectric's registry.
            val sensorLooper = ShadowLooper.getAllLoopers().single { it !in existingLoopers }
            shadowOf(sensorLooper).idle()
            assertTrue(host.supported())
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

    @Test fun luxUsesEventAgeAndCapabilitySurvivesBadSamplesAndShutdown() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val sensor = ShadowSensor.newInstance(Sensor.TYPE_LIGHT)
        shadowOf(context.getSystemService(SensorManager::class.java)).addSensor(sensor)
        val host = LightSensorHost(context)
        assertTrue(host.supported())
        assertTrue(JSONObject(host.sample()).has("problem"))
        SystemClock.sleep(10)
        val event = SensorEventBuilder.newBuilder().setSensor(sensor)
            .setTimestamp(SystemClock.elapsedRealtimeNanos()).setValues(floatArrayOf(213.5f)).build()
        host.onSensorChanged(event)
        assertEquals(213.5, JSONObject(host.sample()).getDouble("lux"), 0.0)
        SystemClock.sleep(1_234)
        assertTrue(JSONObject(host.sample()).getLong("age_ms") >= 1_234)
        event.values[0] = Float.NaN
        host.onSensorChanged(event)
        assertFalse(JSONObject(host.sample()).has("lux"))
        assertTrue(host.supported())
        event.values[0] = 0f
        host.onSensorChanged(event)
        assertEquals(0.0, JSONObject(host.sample()).getDouble("lux"), 0.0)
        host.close()
        host.onSensorChanged(event)
        assertFalse(JSONObject(host.sample()).has("lux"))
        assertTrue(host.supported())
    }
}
