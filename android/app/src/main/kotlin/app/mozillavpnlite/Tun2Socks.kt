package app.mozillavpnlite

import android.os.ParcelFileDescriptor

object Tun2Socks {
    private var activeTunFd: ParcelFileDescriptor? = null

    init {
        try {
            System.loadLibrary("hev-socks5-tunnel")
        } catch (_: Throwable) {}
    }

    @JvmStatic
    private external fun TProxyStartService(configPath: String, fd: Int)

    @JvmStatic
    private external fun TProxyStopService()

    @Synchronized
    fun start(configPath: String, tunFd: ParcelFileDescriptor) {
        stop()
        activeTunFd = tunFd
        try {
            TProxyStartService(configPath, tunFd.fd)
        } catch (_: Throwable) {}
    }

    @Synchronized
    fun stop() {
        val fd = activeTunFd ?: return
        activeTunFd = null
        try {
            TProxyStopService()
        } catch (_: Throwable) {}
        try {
            fd.close()
        } catch (_: Throwable) {}
    }
}
