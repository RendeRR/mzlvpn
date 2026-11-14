package app.mozillavpnlite

import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.Network
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import java.io.File

class MinimalVpnService : VpnService() {

    private var connectivityManager: ConnectivityManager? = null
    private var lastNetworkHandle: Long = 0L

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            val handle = network.networkHandle
            if (lastNetworkHandle != 0L && lastNetworkHandle != handle && isRunning) {
                // Мгновенный сброс пула TLS 1.3 / HTTP/2 при переключении Wi-Fi <-> LTE/5G
                mobile.Mobile.reconnect()
            }
            lastNetworkHandle = handle
        }
    }

    override fun onCreate() {
        super.onCreate()
        connectivityManager = getSystemService(ConnectivityManager::class.java)
        try {
            connectivityManager?.registerDefaultNetworkCallback(networkCallback)
        } catch (_: Exception) {}
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            turnOff(this)
            stopSelf()
            return START_NOT_STICKY
        }
        startCore(this, on = true)
        establishTunnel()
        return START_STICKY
    }

    private fun establishTunnel() {
        val socksPort = mobile.Mobile.socksPort()
        val builder = Builder()
            .setSession("Mozilla VPN Lite")
            .setMtu(1500)
            .addAddress("10.0.0.2", 32)
            .addAddress("fd00::2", 128)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .addDnsServer("1.1.1.1")

        // Исключаем сам VPN-клиент, чтобы HTTP/2 CONNECT к Fastly шёл напрямую
        try {
            builder.addDisallowedApplication(packageName)
        } catch (_: Exception) {}

        val tunFd: ParcelFileDescriptor = builder.establish() ?: return
        val configFile = File(cacheDir, "tproxy.yml").apply {
            writeText(
                """
                tunnel:
                  mtu: 1500
                  ipv4: 10.0.0.2
                  ipv6: 'fd00::2'
                socks5:
                  port: $socksPort
                  address: 127.0.0.1
                  udp: 'udp'
                """.trimIndent()
            )
        }
        Tun2Socks.start(configFile.absolutePath, tunFd)
    }

    override fun onDestroy() {
        try {
            connectivityManager?.unregisterNetworkCallback(networkCallback)
        } catch (_: Exception) {}
        Tun2Socks.stop()
        super.onDestroy()
    }

    companion object {
        const val ACTION_STOP = "app.mozillavpnlite.STOP"

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
            val intent = Intent(context, MinimalVpnService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
            startCore(context, on = true)
            mobile.Mobile.connect()
            isRunning = true
        }

        fun turnOff(context: Context) {
            Tun2Socks.stop()
            if (mobile.Mobile.running()) {
                mobile.Mobile.disconnect()
            }
            isRunning = false
            context.stopService(Intent(context, MinimalVpnService::class.java))
        }
    }
}
