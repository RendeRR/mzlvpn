package app.mozillavpnlite

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import org.json.JSONObject
import java.io.File

class MinimalVpnService : VpnService() {

    private var connectivityManager: ConnectivityManager? = null
    // Underlying physical networks (Wi-Fi / cellular / ethernet). The
    // default-network callback cannot be trusted here: while the VPN is
    // up, the system default network is the VPN itself, whose handle
    // never changes across a Wi-Fi <-> LTE switch — so the core never
    // learned about handovers and its pooled connections went stale.
    // Track the physical network set instead: any change while running
    // means the path under the tunnel changed.
    private val underlyingNetworks = mutableSetOf<Long>()
    private var underlyingSeen = false

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            val changed = synchronized(underlyingNetworks) {
                val added = underlyingNetworks.add(network.networkHandle)
                val wasSeen = underlyingSeen
                underlyingSeen = true
                added && wasSeen
            }
            if (changed && isRunning) reconnectCore()
        }

        override fun onLost(network: Network) {
            val changed = synchronized(underlyingNetworks) {
                underlyingNetworks.remove(network.networkHandle)
            }
            if (changed && isRunning) reconnectCore()
        }
    }

    private fun reconnectCore() {
        Thread {
            try {
                mobile.Mobile.reconnect()
            } catch (_: Throwable) {}
        }.start()
    }

    // Unlocking the phone is the one wake signal that needs no
    // heuristics: whatever silently died while the device slept (server
    // reaped the idle session, NAT state aged out), the pooled sessions
    // are dropped and the next connection dials fresh. The core's
    // suspension watchdog only sees freezes long enough to starve its
    // ticks — light sleep with brief maintenance wakes slips under it,
    // while the upstream session dies all the same. Cost of a false
    // alarm: one H2 redial on the first connection after unlock.
    private val userPresentReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (isRunning) reconnectCore()
        }
    }

    override fun onCreate() {
        super.onCreate()
        ensureNotificationChannel()
        connectivityManager = getSystemService(ConnectivityManager::class.java)
        try {
            val request = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
                .addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
                .build()
            connectivityManager?.registerNetworkCallback(request, networkCallback)
        } catch (_: Exception) {}
        try {
            registerReceiver(userPresentReceiver, IntentFilter(Intent.ACTION_USER_PRESENT))
        } catch (_: Exception) {}
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            isRunning = false
            Thread {
                try {
                    Tun2Socks.stop()
                    if (mobile.Mobile.running()) {
                        mobile.Mobile.disconnect()
                    }
                } catch (_: Throwable) {}
            }.start()
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }

        // Must call startForeground immediately on the main thread to avoid Android ANR
        ensureNotificationChannel()
        startForegroundCompat(
            buildNotification(
                title = getString(R.string.notif_connecting_title),
                text = getString(R.string.notif_connecting_text)
            )
        )

        // Run blocking network & Go tunnel initialization off the Main UI thread.
        // isRunning is set ONLY after the TUN interface is up AND hev-socks5-tunnel
        // confirmed it is forwarding — otherwise the UI would report "connected"
        // while all traffic falls into a dead TUN fd (total blackhole).
        Thread {
            var tunnelUp = false
            try {
                startCore(this, on = false)
                mobile.Mobile.connect()
                val socksPort = mobile.Mobile.socksPort().toInt()
                if (socksPort > 0) {
                    tunnelUp = establishTunnel(socksPort)
                }
            } catch (t: Throwable) {
                android.util.Log.e("MinimalVpnService", "VPN start failed", t)
            }
            if (tunnelUp) {
                isRunning = true
                updateForegroundStatusNotification()
            } else {
                failStop()
            }
        }.start()

        return START_STICKY
    }

    /** Tears everything down after a failed start so no dead state survives. */
    private fun failStop() {
        isRunning = false
        try {
            Tun2Socks.stop()
        } catch (_: Throwable) {}
        try {
            if (mobile.Mobile.running()) {
                mobile.Mobile.disconnect()
            }
        } catch (_: Throwable) {}
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun startForegroundCompat(notification: Notification) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    /**
     * Brings up the TUN interface and starts hev-socks5-tunnel.
     * Returns true only when the native forwarder confirmed it is running.
     *
     * IPv4-only on purpose: the upstream is HTTP/2 CONNECT (TCP) plus a
     * DNS-over-TCP bridge, there is no IPv6/UDP transport yet — an ::/0 route
     * would blackhole all IPv6 traffic instead of letting it fall back.
     */
    private fun establishTunnel(socksPort: Int): Boolean {
        val builder = Builder()
            .setSession(getString(R.string.session_name))
            .setMtu(1400)
            .addAddress("10.0.0.2", 32)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("1.1.1.1")
            .addDnsServer("8.8.8.8")

        // Exclude our own app so HTTP/2 CONNECT to Fastly goes directly over the physical network
        try {
            builder.addDisallowedApplication(packageName)
        } catch (_: Exception) {}

        val tunFd: ParcelFileDescriptor = builder.establish() ?: run {
            android.util.Log.e("MinimalVpnService", "VpnService.Builder.establish() returned null")
            return false
        }
        val configFile = File(cacheDir, "tproxy.yml").apply {
            writeText(
                """
                tunnel:
                  mtu: 1400
                  ipv4: 10.0.0.2
                socks5:
                  port: $socksPort
                  address: 127.0.0.1
                  udp: 'tcp'
                """.trimIndent()
            )
        }
        // Tun2Socks.start closes tunFd itself when the native side refuses.
        return Tun2Socks.start(configFile.absolutePath, tunFd)
    }

    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = getSystemService(NotificationManager::class.java) ?: return
            val channel = NotificationChannel(
                CHANNEL_ID,
                getString(R.string.notif_channel_name),
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = getString(R.string.notif_channel_desc)
                setShowBadge(false)
            }
            nm.createNotificationChannel(channel)
        }
    }

    private fun buildNotification(title: String, text: String): Notification {
        val openAppIntent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val contentPending = PendingIntent.getActivity(
            this,
            1,
            openAppIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val stopIntent = Intent(this, MinimalVpnService::class.java).apply {
            action = ACTION_STOP
        }
        val stopPending = PendingIntent.getService(
            this,
            2,
            stopIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }

        return builder
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentTitle(title)
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(contentPending)
            .addAction(
                Notification.Action.Builder(
                    android.R.drawable.ic_menu_close_clear_cancel,
                    getString(R.string.notif_disconnect),
                    stopPending
                ).build()
            )
            .build()
    }

    private fun updateForegroundStatusNotification() {
        try {
            val raw = mobile.Mobile.statusJSON()
            val root = JSONObject(raw)
            val loc = root.optJSONObject("location")
            val isRu = resources.configuration.locales[0]?.language == "ru"
            val countryName = if (isRu) {
                loc?.optString("name_ru", "")?.ifBlank { null } ?: loc?.optString("name", "Germany") ?: "Germany"
            } else {
                loc?.optString("name", "Germany")?.ifBlank { "Germany" } ?: "Germany"
            }
            val host = loc?.optString("server_host", "fastly-masque.net") ?: "fastly-masque.net"

            var activeEmail = ""
            val accs = root.optJSONArray("accounts")
            if (accs != null) {
                for (i in 0 until accs.length()) {
                    val o = accs.optJSONObject(i) ?: continue
                    if (o.optBoolean("active", false)) {
                        activeEmail = o.optString("email", "")
                        break
                    }
                }
            }

            val title = getString(R.string.notif_connected, countryName)
            val subtitle = if (activeEmail.isNotBlank()) "$activeEmail · $host" else host
            val nm = getSystemService(NotificationManager::class.java)
            nm?.notify(NOTIFICATION_ID, buildNotification(title, subtitle))
        } catch (_: Throwable) {}
    }

    override fun onDestroy() {
        isRunning = false
        try {
            unregisterReceiver(userPresentReceiver)
        } catch (_: Exception) {}
        try {
            connectivityManager?.unregisterNetworkCallback(networkCallback)
        } catch (_: Exception) {}
        Tun2Socks.stop()
        super.onDestroy()
    }

    companion object {
        const val ACTION_STOP = "app.mozillavpnlite.STOP"
        private const val CHANNEL_ID = "mozilla_vpn_lite_status"
        private const val NOTIFICATION_ID = 1001

        @Volatile
        var isRunning = false
            private set

        fun dataDir(context: Context): File = File(context.filesDir, "core")

        @Synchronized
        fun startCore(context: Context, on: Boolean) {
            try {
                val offsetMin = java.util.TimeZone.getDefault().getOffset(System.currentTimeMillis()) / 60000
                mobile.Mobile.setUTCOffset(offsetMin)
            } catch (_: Throwable) {}
            if (!mobile.Mobile.running()) {
                val lang = context.resources.configuration.locales[0]?.language ?: "en"
                mobile.Mobile.start(dataDir(context).absolutePath, on, lang)
            }
        }

        fun turnOn(context: Context) {
            val intent = Intent(context.applicationContext, MinimalVpnService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.applicationContext.startForegroundService(intent)
            } else {
                context.applicationContext.startService(intent)
            }
        }

        fun turnOff(context: Context) {
            val intent = Intent(context.applicationContext, MinimalVpnService::class.java).apply {
                action = ACTION_STOP
            }
            context.applicationContext.startService(intent)
        }
    }
}
