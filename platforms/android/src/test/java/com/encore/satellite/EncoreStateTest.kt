package com.encore.satellite

import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class EncoreStateTest {
    @Test fun idleAnnouncementRetainsSharedOwnershipUntilDrainCompletes() {
        val announcement = EncoreState.parse(snapshot(), EncoreState(running = true))
        assertEquals("idle", announcement.phase)
        assertTrue(announcement.voiceActive)
        assertTrue(announcement.announcing)
        assertEquals("announcement", announcement.outputOwner)
        assertEquals("playing", announcement.outputState)
        assertFalse(announcement.talkAllowed)
        assertFalse(announcement.microphoneVisible)

        val draining = EncoreState.parse(snapshot(announcing = false, output = "draining"), announcement)
        assertTrue(draining.voiceActive)
        assertFalse(draining.announcing)
        assertEquals("announcement", draining.outputOwner)
        assertFalse(draining.talkAllowed)

        val idle = EncoreState.parse(snapshot(active = false, announcing = false, output = "", owner = "", talkAllowed = true), draining)
        assertFalse(idle.voiceActive)
        assertTrue(idle.talkAllowed)
        assertEquals("", idle.outputOwner)
        assertEquals("", idle.outputState)
    }

    @Test fun sharedPermissionIsNotInferredFromIdleOrConnection() {
        val inhibited = EncoreState.parse(snapshot(active = false, announcing = false, output = "", owner = ""), EncoreState())
        assertTrue(inhibited.connected)
        assertFalse(inhibited.voiceActive)
        assertFalse(inhibited.talkAllowed)
    }

    companion object {
        fun snapshot(active: Boolean = true, announcing: Boolean = true, output: String = "playing",
            owner: String = "announcement", talkAllowed: Boolean = false) = """
            {"voice":{"phase":"idle","connected":true,"muted":false,"alert":false,
              "active":$active,"announcing":$announcing,"talk_allowed":$talkAllowed,
              "output":{"state":"$output","owner":"$owner","received":true}},
              "models":2,"dashboard":"","reload":0,"problem":""}
        """.trimIndent()
    }
}
