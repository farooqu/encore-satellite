package com.encore.satellite

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import com.sendspindroid.sendspin.SendSpinHost
import com.sendspindroid.sendspin.crypto.*
import com.sendspindroid.sendspin.pairing.PairingOutcome
import com.sendspindroid.sendspin.pairing.PairingToken
import org.json.JSONArray
import org.json.JSONObject
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Independent Sendspin identity/trust. Keystore failure is surfaced, never downgraded to plaintext. */
internal class MusicHost(context: Context, private val key: SecretKey = loadMusicKey()) : SendSpinHost {
    private val prefs = context.getSharedPreferences("sendspin", Context.MODE_PRIVATE)

    @Synchronized private fun read(name: String): String? = prefs.getString(name, null)?.let {
        val bytes = Base64.decode(it, Base64.NO_WRAP)
        Cipher.getInstance("AES/GCM/NoPadding").run {
            init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
            String(doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8)
        }
    }

    private fun encrypt(value: String): String {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key)
        return Base64.encodeToString(cipher.iv + cipher.doFinal(value.toByteArray()), Base64.NO_WRAP)
    }

    @Synchronized private fun write(name: String, value: String) = prefs.edit().putString(name, encrypt(value)).commit()

    private val identity = read("identity")?.let { requireNotNull(ClientIdentity.fromStoredKey(it)) }
        ?: ClientIdentity.generate().also { check(write("identity", ClientIdentity.encodeForStorage(it))) }
    private val config = read("pairing")?.let {
        PairingConfig(requireNotNull(Base64Url.decodeOrNull(it)), false, false)
    } ?: newPairingConfig()
        .also { check(write("pairing", Base64Url.encode(it.pairingPsk))) }

    private var retired = false
    private val trust = newTrustStore(read("records")?.let { raw ->
        val array = JSONArray(raw)
        (0 until array.length()).map { index ->
            val record = array.getJSONObject(index)
            PskRecord(record.getString("id"), requireNotNull(Base64Url.decodeOrNull(record.getString("psk"))),
                if (record.isNull("server")) null else record.getString("server"), record.getBoolean("used"))
        }
    }.orEmpty())

    private fun newPairingConfig() = PairingConfig(ByteArray(32).also(SecureRandom()::nextBytes), false, false)

    private fun newTrustStore(records: List<PskRecord>): TrustStore = object : InMemoryTrustStore(records, config.pairingPskId) {
        override fun onChanged(): Boolean = synchronized(this@MusicHost) {
            // A retired connection must not write old records back after rotation.
            !retired && write("records", JSONArray().apply {
                listRecords().forEach { record -> put(JSONObject().put("id", record.pskId)
                    .put("psk", Base64Url.encode(record.psk)).put("server", record.serverId).put("used", record.used)) }
            }.toString())
        }
    }
    private val pairing = object : PairingConfigStore {
        override fun load() = config
        // Only a deliberate local revoke operation may rotate the token.
        override fun setUnpairedAccess(enabled: Boolean) = !enabled
        override fun rotatePairingPsk(newPsk: ByteArray, claimedPskIds: Set<String>) = PairingConfigStore.RotateResult.Invalid
    }

    val pairingClientId: String get() = identity.clientId
    fun pairingToken(): String = PairingToken.encode(identity.publicKey, config.pairingPsk)

    /** Retire this host after shutting down playback. Keep the device's public identity. */
    @Synchronized fun revokeAll() {
        val next = newPairingConfig()
        check(prefs.edit().putString("pairing", encrypt(Base64Url.encode(next.pairingPsk))).remove("records").commit())
        retired = true
    }

    var playerVolume: Int
        get() = prefs.getInt("volume", 100)
        set(value) { prefs.edit().putInt("volume", value).apply() }
    var playerMuted: Boolean
        get() = prefs.getBoolean("muted", false)
        set(value) { prefs.edit().putBoolean("muted", value).apply() }

    override val softwareVersion = "Encore ${BuildConfig.CORE_VERSION}"
    // The SDK advertises its artwork role only in standard memory mode.
    override val lowMemoryMode = false
    override val highPowerMode = false
    override val serverColors = false
    override fun preferredCodec() = "pcm"
    override fun outputDelayMs() = prefs.getInt("delay", 0)
    override fun setOutputDelayMs(delayMs: Int) { check(prefs.edit().putInt("delay", delayMs).commit()) }
    override fun clientIdentity() = identity
    override fun trustStore(): TrustStore = trust
    override fun pairingConfigStore(): PairingConfigStore = pairing
    override fun pairingCodeFailures() = prefs.getInt("failures", 0)
    override fun setPairingCodeFailures(value: Int) { check(prefs.edit().putInt("failures", value).commit()) }
    override fun rememberServerName(serverId: String, name: String) {}
    override fun forgetServerName(serverId: String) {}
    override fun onPairingOutcome(outcome: PairingOutcome) {}
}

private fun loadMusicKey(): SecretKey = KeyStore.getInstance("AndroidKeyStore").let { store ->
    store.load(null)
    (store.getKey("encore-sendspin", null) as SecretKey?) ?: KeyGenerator.getInstance(
        KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
        init(KeyGenParameterSpec.Builder("encore-sendspin", KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build())
        generateKey()
    }
}
