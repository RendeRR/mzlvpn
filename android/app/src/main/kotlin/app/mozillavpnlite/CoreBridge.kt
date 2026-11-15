package app.mozillavpnlite

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

data class AccountItem(
    val id: String,
    val email: String,
    val position: Int,
    val active: Boolean,
    val enabled: Boolean,
    val exhausted: Boolean,
    val usedBytes: Long,
    val limitBytes: Long
)

data class LocationItem(
    val code: String,
    val flag: String,
    val name: String,
    val city: String,
    val host: String,
    val port: Int,
    val latencyMs: Int
)

data class ActionOutcome(
    val ok: Boolean,
    val state: String,
    val message: String,
    val email: String = ""
)

data class CoreSnapshot(
    val state: String = "disconnected",
    val selectedLocation: String = "DE",
    val protocol: String = "h2",
    val activeAccount: Int = 0,
    val thresholdPct: Int = 95,
    val accounts: List<AccountItem> = emptyList(),
    val locations: List<LocationItem> = emptyList(),
    val quotaEvents: List<String> = emptyList()
)

object CoreBridge {
    private const val FIVE_GB_BYTES = 5368709120L

    fun init(context: Context) {
        val appCtx = context.applicationContext
        Thread {
            try {
                MinimalVpnService.startCore(appCtx, on = false)
            } catch (_: Throwable) {}
        }.start()
    }

    fun countryFlag(code: String): String {
        val clean = code.trim().uppercase()
        if (clean.length != 2) return "🌐"
        val first = Character.codePointAt(clean, 0) - 0x41 + 0x1F1E6
        val second = Character.codePointAt(clean, 1) - 0x41 + 0x1F1E6
        return String(Character.toChars(first)) + String(Character.toChars(second))
    }

    fun formatBytes(bytes: Long): String {
        val safe = bytes.coerceAtLeast(0L)
        val gb = safe.toDouble() / (1024.0 * 1024.0 * 1024.0)
        if (gb >= 0.1) {
            return String.format("%.2f ГБ", gb)
        }
        val mb = safe.toDouble() / (1024.0 * 1024.0)
        if (mb >= 1.0) {
            return String.format("%.0f МБ", mb)
        }
        if (safe > 0L) {
            val kb = safe.toDouble() / 1024.0
            return String.format("%.0f КБ", kb.coerceAtLeast(1.0))
        }
        return "0 МБ"
    }

    fun formatPing(latencyMs: Int): String {
        return if (latencyMs > 0) "$latencyMs мс" else "…"
    }

    private fun parseActionOutcome(rawJson: String): ActionOutcome {
        return try {
            val obj = JSONObject(rawJson)
            val state = obj.optString("state", "error")
            ActionOutcome(
                ok = state == "ok",
                state = state,
                message = obj.optString("message", "Неизвестный ответ ядра"),
                email = obj.optString("email", "")
            )
        } catch (t: Throwable) {
            ActionOutcome(
                ok = false,
                state = "error",
                message = t.message ?: "Ошибка разбора ответа ядра"
            )
        }
    }

    suspend fun getSnapshot(): CoreSnapshot = withContext(Dispatchers.IO) {
        fetchSnapshotSync()
    }

    private fun fetchSnapshotSync(): CoreSnapshot {
        return try {
            val raw = mobile.Mobile.statusJSON()
            val root = JSONObject(raw)
            val accArr = root.optJSONArray("accounts") ?: JSONArray()
            val locArr = root.optJSONArray("locations") ?: JSONArray()

            val accounts = mutableListOf<AccountItem>()
            var activeIdx = 0
            for (i in 0 until accArr.length()) {
                val o = accArr.optJSONObject(i) ?: continue
                val usage = o.optJSONObject("usage")
                val rawLimit = usage?.optLong("limit_bytes", FIVE_GB_BYTES) ?: FIVE_GB_BYTES
                val limit = if (rawLimit <= 0L) FIVE_GB_BYTES else rawLimit
                val rem = if (usage != null && rawLimit > 0L) {
                    usage.optLong("remaining_bytes", limit).coerceIn(0L, limit)
                } else {
                    limit
                }
                val used = (limit - rem).coerceAtLeast(0L)
                val isActive = o.optBoolean("active", false)
                if (isActive) {
                    activeIdx = i
                }
                val statusStr = o.optString("status", "ready")

                accounts.add(
                    AccountItem(
                        id = o.optString("id"),
                        email = o.optString("email"),
                        position = o.optInt("position", i + 1),
                        active = isActive,
                        enabled = o.optBoolean("enabled", true),
                        exhausted = statusStr == "reserved" || rem <= 0L,
                        usedBytes = used,
                        limitBytes = limit
                    )
                )
            }

            val locations = mutableListOf<LocationItem>()
            for (i in 0 until locArr.length()) {
                val o = locArr.optJSONObject(i) ?: continue
                val code = o.optString("code", "DE")
                val nameEn = o.optString("name", code)
                val nameRu = o.optString("name_ru", "").ifBlank { nameEn }
                val ping = o.optLong("ping_ms", 0L).toInt().coerceAtLeast(0)

                locations.add(
                    LocationItem(
                        code = code,
                        flag = countryFlag(code),
                        name = nameRu,
                        city = o.optString("city", nameEn),
                        host = o.optString("server_host", "muc139.m1.fastly-masque.net"),
                        port = o.optInt("server_port", 2499),
                        latencyMs = ping
                    )
                )
            }

            // "Connected" only when ALL layers are really up: Go core reports
            // connected AND the VPN service finished starting AND the native
            // tun2socks forwarder is alive. Any single flag alone lies.
            val isConnected = root.optBoolean("connected", false) &&
                MinimalVpnService.isRunning && Tun2Socks.isRunning()
            val protoRaw = root.optString("tunnel_protocol", "HTTP/2 CONNECT (TLS 1.3)")
            val protoShort = if (protoRaw.contains("HTTP/2", ignoreCase = true) || protoRaw == "h2") "h2" else "h3"
            val selLoc = root.optString("selected_location", "DE").let {
                when {
                    it.isBlank() || it.equals("REC", ignoreCase = true) -> "recommended"
                    else -> it
                }
            }

            val quotaEvents = mutableListOf<String>()
            val evArr = root.optJSONArray("quota_events") ?: JSONArray()
            for (i in 0 until evArr.length()) {
                val o = evArr.optJSONObject(i) ?: continue
                val err = o.optString("error", "")
                val sum = o.optString("summary", "")
                val body = when {
                    err.isNotBlank() -> "ОШИБКА: $err"
                    sum.isNotBlank() -> sum
                    else -> "заголовков квоты в ответе нет"
                }
                val status = o.optInt("status", 0)
                val statusPart = if (status > 0) " · HTTP $status" else ""
                quotaEvents.add("${o.optString("at", "")} · ${o.optString("source", "")}$statusPart · $body")
            }

            CoreSnapshot(
                state = if (isConnected) "connected" else "disconnected",
                selectedLocation = selLoc,
                protocol = protoShort,
                activeAccount = activeIdx,
                thresholdPct = root.optInt("threshold_percent", 95),
                accounts = accounts,
                locations = locations,
                quotaEvents = quotaEvents
            )
        } catch (_: Throwable) {
            CoreSnapshot()
        }
    }

    suspend fun refreshServers() = withContext(Dispatchers.IO) {
        try {
            // PingAllLocations is automatically launched in mobile.Start()
        } catch (_: Throwable) {}
    }

    suspend fun setProtocol(proto: String) = withContext(Dispatchers.IO) {
        try {
            val label = if (proto == "h2") "HTTP/2 CONNECT (TLS 1.3)" else "HTTP/3 MASQUE (QUIC)"
            mobile.Mobile.setProtocol(label)
        } catch (_: Throwable) {}
    }

    suspend fun selectAccount(idx: Int) = withContext(Dispatchers.IO) {
        try {
            val snap = fetchSnapshotSync()
            val acc = snap.accounts.getOrNull(idx) ?: return@withContext
            mobile.Mobile.switchAccount(acc.id)
        } catch (_: Throwable) {}
    }

    suspend fun selectLocation(code: String) = withContext(Dispatchers.IO) {
        try {
            val mapped = if (code == "recommended") "REC" else code
            mobile.Mobile.setLocation(mapped)
        } catch (_: Throwable) {}
    }

    suspend fun signInPassword(
        email: String,
        password: String,
        unblockCode: String
    ): ActionOutcome = withContext(Dispatchers.IO) {
        try {
            val raw = mobile.Mobile.signInPassword(email, password, unblockCode)
            parseActionOutcome(raw)
        } catch (t: Throwable) {
            ActionOutcome(
                ok = false,
                state = "error",
                message = t.message ?: "Ошибка входа в аккаунт Mozilla"
            )
        }
    }

    suspend fun verifySignInCode(code: String): ActionOutcome = withContext(Dispatchers.IO) {
        try {
            val raw = mobile.Mobile.verifySignInCode(code)
            parseActionOutcome(raw)
        } catch (t: Throwable) {
            ActionOutcome(
                ok = false,
                state = "error",
                message = t.message ?: "Ошибка проверки 6-значного кода"
            )
        }
    }

    suspend fun addAccountSessionToken(
        email: String,
        sessionToken: String
    ): ActionOutcome = withContext(Dispatchers.IO) {
        try {
            val raw = mobile.Mobile.addAccountSessionToken(email, sessionToken)
            parseActionOutcome(raw)
        } catch (t: Throwable) {
            ActionOutcome(
                ok = false,
                state = "error",
                message = t.message ?: "Ошибка добавления по sessionToken"
            )
        }
    }

    suspend fun moveAccount(idx: Int, delta: Int) = withContext(Dispatchers.IO) {
        try {
            val snap = fetchSnapshotSync()
            val acc = snap.accounts.getOrNull(idx) ?: return@withContext
            mobile.Mobile.moveAccount(acc.id, delta.toLong())
        } catch (_: Throwable) {}
    }

    suspend fun toggleAccountEnabled(idx: Int) = withContext(Dispatchers.IO) {
        try {
            val snap = fetchSnapshotSync()
            val acc = snap.accounts.getOrNull(idx) ?: return@withContext
            mobile.Mobile.toggleAccountEnabled(acc.id)
        } catch (_: Throwable) {}
    }

    suspend fun removeAccount(idx: Int) = withContext(Dispatchers.IO) {
        try {
            val snap = fetchSnapshotSync()
            val acc = snap.accounts.getOrNull(idx) ?: return@withContext
            mobile.Mobile.removeAccount(acc.id)
        } catch (_: Throwable) {}
    }

    suspend fun setThreshold(pct: Int) = withContext(Dispatchers.IO) {
        try {
            mobile.Mobile.setThreshold(pct.toLong())
        } catch (_: Throwable) {}
    }

    suspend fun pingAll() = withContext(Dispatchers.IO) {
        try {
            mobile.Mobile.pingAllLocations()
        } catch (_: Throwable) {}
    }
}
