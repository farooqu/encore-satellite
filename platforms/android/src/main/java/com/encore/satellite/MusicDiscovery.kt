package com.encore.satellite

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.os.Handler
import android.os.Looper
import java.net.InetAddress
import java.net.URI

internal data class MusicEndpoint(val address: String, val path: String) {
    val setting get() = if (path == "/sendspin") address else address + path

    companion object {
        private val pathPattern = Regex("/[A-Za-z0-9._~/-]{0,200}")

        fun manual(input: String): MusicEndpoint? {
            val value = input.trim()
            if (value.length > 255) return null
            val uri = runCatching { URI("ws://$value") }.getOrNull() ?: return null
            if (uri.host == null || uri.port !in 1..65535 || uri.rawUserInfo != null ||
                uri.rawQuery != null || uri.rawFragment != null || '%' in uri.rawAuthority) return null
            val path = uri.rawPath.ifEmpty { "/sendspin" }
            return if (pathPattern.matches(path)) MusicEndpoint(uri.rawAuthority, path) else null
        }

        fun advertised(host: InetAddress?, port: Int, path: String?): MusicEndpoint? {
            if (host == null || host.isLinkLocalAddress || host.isAnyLocalAddress || host.isMulticastAddress || port !in 1..65535) return null
            val address = host.hostAddress ?: return null
            val endpointPath = path ?: "/sendspin"
            if ('%' in address || !pathPattern.matches(endpointPath)) return null
            return MusicEndpoint(if (':' in address) "[$address]:$port" else "$address:$port", endpointPath)
        }
    }
}

/** A bounded browse. Do not connect before the window closes, or guess between servers. */
@Suppress("DEPRECATION") // resolveService keeps one serial resolve path on API 26+.
internal class MusicDiscovery(context: Context, private val finished: (MusicEndpoint?, String) -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    private val nsd = context.getSystemService(NsdManager::class.java)
    private val wifi = context.applicationContext.getSystemService(WifiManager::class.java)
    private var lock: WifiManager.MulticastLock? = null
    private var listener: NsdManager.DiscoveryListener? = null
    private var resolving = false
    private val pending = linkedMapOf<String, NsdServiceInfo>()
    private val results = linkedMapOf<String, MusicEndpoint?>()
    private var incomplete = false

    private fun finish(complete: Boolean) {
        val endpoints = if (complete && !incomplete && pending.isEmpty()) results.values.filterNotNull() else null
        stop()
        val message = when {
            endpoints == null -> "Could not complete Sendspin discovery"
            endpoints.isEmpty() -> "No Sendspin server found on this network"
            endpoints.size > 1 -> "Multiple Sendspin servers found. You can specify a server below."
            else -> ""
        }
        finished(endpoints?.singleOrNull(), message)
    }

    fun start() {
        if (listener != null) return
        val browse = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(type: String) {}
            override fun onDiscoveryStopped(type: String) = whileCurrent(this) { finish(false) }
            override fun onStartDiscoveryFailed(type: String, code: Int) = whileCurrent(this) { finish(false) }
            override fun onStopDiscoveryFailed(type: String, code: Int) {}
            override fun onServiceFound(info: NsdServiceInfo) = whileCurrent(this) {
                if (info.serviceName !in results && info.serviceName !in pending) {
                    if (pending.size + results.size == 16) incomplete = true
                    else { pending[info.serviceName] = info; resolveNext() }
                }
            }
            override fun onServiceLost(info: NsdServiceInfo) = whileCurrent(this) {
                pending.remove(info.serviceName)
                results.remove(info.serviceName)
            }
        }
        incomplete = false
        listener = browse
        try {
            lock = wifi?.createMulticastLock("encore-sendspin")?.also { it.acquire() }
            requireNotNull(nsd).discoverServices("_sendspin-server._tcp.", NsdManager.PROTOCOL_DNS_SD, browse)
            main.postDelayed({ if (listener === browse) finish(true) }, 5_000)
        } catch (_: Exception) {
            finish(false)
        }
    }

    fun stop() {
        val browse = listener
        listener = null
        if (browse != null) runCatching { nsd?.stopServiceDiscovery(browse) }
        lock?.let { if (it.isHeld) it.release() }
        lock = null
        pending.clear()
        results.clear()
    }

    private fun whileCurrent(browse: NsdManager.DiscoveryListener, action: () -> Unit) {
        main.post { if (listener === browse) action() }
    }

    private fun resolveNext() {
        val browse = listener ?: return
        if (resolving) return
        val info = pending.values.firstOrNull() ?: return
        resolving = true
        fun finished(resolved: NsdServiceInfo?) {
            main.post {
                resolving = false
                if (listener === browse && pending[info.serviceName] === info) {
                    pending.remove(info.serviceName)
                    if (resolved == null) incomplete = true
                    results[info.serviceName] = resolved?.let {
                        MusicEndpoint.advertised(it.host, it.port, it.attributes["path"]?.toString(Charsets.UTF_8))
                    }
                }
                resolveNext()
            }
        }
        runCatching {
            requireNotNull(nsd).resolveService(info, object : NsdManager.ResolveListener {
                override fun onResolveFailed(info: NsdServiceInfo, code: Int) = finished(null)
                override fun onServiceResolved(info: NsdServiceInfo) = finished(info)
            })
        }.onFailure { finished(null) }
    }
}
