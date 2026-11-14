package app.mozillavpnlite

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.Network
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import org.json.JSONObject
import java.io.File

class MinimalVpnService : VpnService() {

    private var connectivityManager: ConnectivityManager? = null
    private var lastNetworkHandle: Long = 0L

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            val handle = network.networkHandle
            if (lastNetworkHandle != 0L && lastNetworkHandle != handle && isRunning) {
                Thread {
                    try {
                        mobile.Mobile.reconnect()
                    } catch (_: Throwable) {}
                }.start()
            }
            lastNetworkHandle = handle
        }
    }

    override fun onCreate() {
        super.onCreate()
        ensureNotificationChannel()
        connectivityManager = getSystemService(ConnectivityManager::class.java)
        try {
            connectivityManager?.registerDefaultNetworkCallback(networkCallback)
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
        startForeground(
            NOTIFICATION_ID,
            buildNotification(
                title = "Mozilla VPN Lite · Подключение…",
                text = "Запуск локального туннеля и проверка ProxyPass"
            )
        )
        isRunning = true

        // Run blocking network & Go tunnel initialization off the Main UI thread
        Thread {
            try {
                startCore(this, on = false)
                mobile.Mobile.connect()
                val socksPort = mobile.Mobile.socksPort().toInt()
                if (socksPort > 0) {
                    establishTunnel(socksPort)
                    updateForegroundStatusNotification()
                } else {
                    isRunning = false
                    stopForeground(STOP_FOREGROUND_REMOVE)
                    stopSelf()
                }
            } catch (_: Throwable) {
                isRunning = false
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }.start()

        return START_STICKY
    }

    private fun establishTunnel(socksPort: Int) {
        val builder = Builder()
            .setSession("Mozilla VPN Lite")
            .setMtu(1400)
            .addAddress("10.0.0.2", 32)
            .addAddress("fd00::2", 128)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .addDnsServer("1.1.1.1")
            .addDnsServer("8.8.8.8")

        // Exclude our own app so HTTP/2 CONNECT to Fastly goes directly over the physical network
        try {
            builder.addDisallowedApplication(packageName)
        } catch (_: Exception) {}

        val tunFd: ParcelFileDescriptor = builder.establish() ?: return
        val configFile = File(cacheDir, "tproxy.yml").apply {
            writeText(
                """
                tunnel:
                  mtu: 1400
                  ipv4: 10.0.0.2
                  ipv6: 'fd00::2'
                socks5:
                  port: $socksPort
                  address: 127.0.0.1
                  udp: 'tcp'
                """.trimIndent()
            )
        }
        Tun2Socks.start(configFile.absolutePath, tunFd)
    }

    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = getSystemService(NotificationManager::class.java) ?: return
            val channel = NotificationChannel(
                CHANNEL_ID,
                "Статус туннеля Mozilla VPN Lite",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Показывает активный сервер Fastly и профиль Mozilla VPN"
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
                    "Отключить",
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
            val countryRu = loc?.optString("name_ru", "Германия")?.ifBlank { "Германия" } ?: "Германия"
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

            val title = "Подключено · $countryRu"
            val subtitle = if (activeEmail.isNotBlank()) "$activeEmail · $host" else host
            val nm = getSystemService(NotificationManager::class.java)
            nm?.notify(NOTIFICATION_ID, buildNotification(title, subtitle))
        } catch (_: Throwable) {}
    }

    override fun onDestroy() {
        isRunning = false
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
            if (!mobile.Mobile.running()) {
                mobile.Mobile.start(dataDir(context).absolutePath, on)
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
