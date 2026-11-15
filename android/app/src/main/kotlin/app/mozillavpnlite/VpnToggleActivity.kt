package app.mozillavpnlite

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import android.widget.Toast
import org.json.JSONObject

/**
 * Invisible entry point shared by the home-screen widget and the
 * launcher long-press shortcut: one tap toggles the tunnel.
 *
 * - Running (or starting) -> stop.
 * - Stopped -> start with the persisted account/location selection,
 *   asking for the system VPN consent first when it is missing.
 * - No accounts yet -> open the app; there is nothing to connect with.
 */
class VpnToggleActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        if (MinimalVpnService.isRunning || MinimalVpnService.isStarting) {
            MinimalVpnService.turnOff(this)
            Toast.makeText(this, R.string.toggle_toast_off, Toast.LENGTH_SHORT).show()
            finish()
            return
        }

        // The account check needs the core loaded: in a cold process
        // the pool is still on disk, so startCore first (fast, file IO
        // only), then decide back on the UI thread.
        Thread {
            val hasAccounts = try {
                MinimalVpnService.startCore(this, on = false)
                val root = JSONObject(mobile.Mobile.statusJSON())
                (root.optJSONArray("accounts")?.length() ?: 0) > 0
            } catch (_: Throwable) {
                true // On any doubt, follow the normal connect flow.
            }
            runOnUiThread {
                if (isFinishing) return@runOnUiThread
                if (!hasAccounts) {
                    startActivity(
                        Intent(this, MainActivity::class.java)
                            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    )
                    finish()
                } else {
                    requestConnect()
                }
            }
        }.start()
    }

    private fun requestConnect() {
        val prepareIntent = VpnService.prepare(this)
        if (prepareIntent != null) {
            @Suppress("DEPRECATION")
            startActivityForResult(prepareIntent, REQUEST_VPN_CONSENT)
        } else {
            connectNow()
        }
    }

    private fun connectNow() {
        MinimalVpnService.turnOn(this)
        Toast.makeText(this, R.string.toggle_toast_on, Toast.LENGTH_SHORT).show()
        finish()
    }

    @Deprecated("Plain Activity result flow is enough for a single consent prompt")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQUEST_VPN_CONSENT && resultCode == RESULT_OK) {
            connectNow()
        } else {
            finish()
        }
    }

    companion object {
        private const val REQUEST_VPN_CONSENT = 41
    }
}
