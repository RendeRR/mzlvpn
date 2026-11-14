package app.mozillavpnlite

import org.json.JSONArray
import org.json.JSONObject

data class AccountItem(
    val id: String,
    val email: String,
    val position: Int,
    val active: Boolean,
    val status: String,
    val usedPercent: Int,
    val remainingGb: String
)

data class LocationItem(
    val code: String,
    val name: String,
    val city: String,
    val serverHost: String,
    val pingMs: Long
)

data class CoreSnapshot(
    val connected: Boolean = false,
    val selectedLocation: String = "DE",
    val activeLocationName: String = "Germany",
    val activeServerHost: String = "fra104.m1.fastly-masque.net:2499",
    val thresholdPercent: Int = 95,
    val accounts: List<AccountItem> = emptyList(),
    val locations: List<LocationItem> = emptyList()
)

object CoreBridge {
    fun fetchSnapshot(): CoreSnapshot {
        return try {
            val raw = mobile.Mobile.statusJSON()
            val root = JSONObject(raw)
            val locObj = root.optJSONObject("location")
            val accArr = root.optJSONArray("accounts") ?: JSONArray()
            val locArr = root.optJSONArray("locations") ?: JSONArray()

            val accounts = mutableListOf<AccountItem>()
            for (i in 0 until accArr.length()) {
                val o = accArr.optJSONObject(i) ?: continue
                val usage = o.optJSONObject("usage")
                val limit = usage?.optLong("limit_bytes", 5368709120L) ?: 5368709120L
                val rem = usage?.optLong("remaining_bytes", limit) ?: limit
                val usedPct = if (limit > 0) (((limit - rem) * 100L) / limit).toInt() else 0
                val remGb = String.format("%.2f ГБ", rem.toDouble() / (1024.0 * 1024.0 * 1024.0))
                accounts.add(
                    AccountItem(
                        id = o.optString("id"),
                        email = o.optString("email"),
                        position = o.optInt("position", i + 1),
                        active = o.optBoolean("active", false),
                        status = o.optString("status", "ready"),
                        usedPercent = usedPct,
                        remainingGb = remGb
                    )
                )
            }

            val locations = mutableListOf<LocationItem>()
            for (i in 0 until locArr.length()) {
                val o = locArr.optJSONObject(i) ?: continue
                locations.add(
                    LocationItem(
                        code = o.optString("code"),
                        name = o.optString("name"),
                        city = o.optString("city"),
                        serverHost = o.optString("server_host"),
                        pingMs = o.optLong("ping_ms", 0L)
                    )
                )
            }

            CoreSnapshot(
                connected = root.optBoolean("connected", false),
                selectedLocation = root.optString("selected_location", "DE"),
                activeLocationName = locObj?.optString("name", "Germany") ?: "Germany",
                activeServerHost = "${locObj?.optString("server_host", "fra104.m1.fastly-masque.net")}:${locObj?.optInt("server_port", 2499) ?: 2499}",
                thresholdPercent = root.optInt("threshold_percent", 95),
                accounts = accounts,
                locations = locations
            )
        } catch (_: Throwable) {
            CoreSnapshot()
        }
    }
}
