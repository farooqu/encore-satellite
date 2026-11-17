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
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Independent Sendspin identity/trust. Keystore failure is surfaced, never downgraded to plaintext. */
class MusicHost(context: Context) : SendSpinHost {
    private val prefs = context.getSharedPreferences("sendspin", Context.MODE_PRIVATE)
    private val key: SecretKey = KeyStore.getInstance("AndroidKeyStore").let { store ->
        store.load(null)
        (store.getKey("encore-sendspin", null) as SecretKey?) ?: KeyGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(KeyGenParameterSpec.Builder("encore-sendspin", KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build())
            generateKey()
        }
    }

    @Synchronized private fun read(name: String): String? = prefs.getString(name, null)?.let {
        val bytes = Base64.decode(it, Base64.NO_WRAP)
        Cipher.getInstance("AES/GCM/NoPadding").run {
            init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
            String(doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8)
        }
    }

    @Synchronized private fun write(name: String, value: String): Boolean {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key)
        return prefs.edit().putString(name, Base64.encodeToString(cipher.iv + cipher.doFinal(value.toByteArray()), Base64.NO_WRAP)).commit()
    }

    private val identity = read("identity")?.let { requireNotNull(ClientIdentity.fromStoredKey(it)) }
        ?: ClientIdentity.generate().also { check(write("identity", ClientIdentity.encodeForStorage(it))) }
    private var config = read("pairing")?.let {
        PairingConfig(requireNotNull(Base64Url.decodeOrNull(it)), false, false)
    } ?: PairingConfig(ByteArray(32).also { java.security.SecureRandom().nextBytes(it) }, false, false)
        .also { check(write("pairing", Base64Url.encode(it.pairingPsk))) }

    private val trust = object : InMemoryTrustStore(initial = read("records")?.let { raw ->
        val array = JSONArray(raw)
        (0 until array.length()).map { index ->
            val record = array.getJSONObject(index)
            PskRecord(record.getString("id"), requireNotNull(Base64Url.decodeOrNull(record.getString("psk"))),
                record.getString("server"), record.getBoolean("used"))
        }
    } ?: emptyList(), pairingPskId = config.pairingPskId) {
        override fun onChanged(): Boolean = write("records", JSONArray().apply {
            listRecords().forEach { record -> put(JSONObject().put("id", record.pskId)
                .put("psk", Base64Url.encode(record.psk)).put("server", record.serverId).put("used", record.used)) }
        }.toString())
    }
    private val pairing = object : PairingConfigStore {
        override fun load() = config
        // Initial package offers the persistent pairing token, not unpaired access or rotation UI.
        override fun setUnpairedAccess(enabled: Boolean) = !enabled
        override fun rotatePairingPsk(newPsk: ByteArray, claimedPskIds: Set<String>) = PairingConfigStore.RotateResult.Invalid
    }

    fun pairingToken(): String = PairingToken.encode(identity.publicKey, config.pairingPsk)
    override val softwareVersion = "Encore 0.1.0"
    override val lowMemoryMode = true
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
