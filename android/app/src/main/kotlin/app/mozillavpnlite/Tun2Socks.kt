package app.mozillavpnlite

import android.os.ParcelFileDescriptor
import android.util.Log

/**
 * JNI bridge to hev-socks5-tunnel.
 *
 * The native library registers its methods in JNI_OnLoad via RegisterNatives
 * with an ALL-OR-NOTHING table of four methods. If any Kotlin declaration does
 * not match the native signature exactly (name AND return type), registration
 * fails, JNI_OnLoad returns JNI_ERR and System.loadLibrary throws — so all
 * four methods must be declared here with the exact signatures from hev-jni.c:
 *
 *   TProxyStartService (Ljava/lang/String;I)Z   -> Boolean
 *   TProxyStopService  ()Z                     -> Boolean
 *   TProxyIsRunning    ()Z                     -> Boolean
 *   TProxyGetStats     ()[J                    -> LongArray
 *
 * Failures must NEVER be swallowed silently: a VPN that routes all traffic
 * into a TUN fd nobody reads is a total blackhole.
 */
object Tun2Socks {
    private const val TAG = "Tun2Socks"

    @Volatile
    private var libraryLoaded = false

    private var activeTunFd: ParcelFileDescriptor? = null

    init {
        libraryLoaded = try {
            System.loadLibrary("hev-socks5-tunnel")
            true
        } catch (t: Throwable) {
            Log.e(TAG, "failed to load hev-socks5-tunnel", t)
            false
        }
    }

    private external fun TProxyStartService(configPath: String, fd: Int): Boolean
    private external fun TProxyStopService(): Boolean
    private external fun TProxyIsRunning(): Boolean
    private external fun TProxyGetStats(): LongArray

    /**
     * Starts the tun2socks forwarder. Returns true only when the native side
     * confirmed the start. On any failure the passed [tunFd] is closed so the
     * caller must tear the VPN down instead of blackholing the device.
     */
    @Synchronized
    fun start(configPath: String, tunFd: ParcelFileDescriptor): Boolean {
        stop()
        if (!libraryLoaded) {
            Log.e(TAG, "hev-socks5-tunnel library is not loaded, cannot start")
            closeQuietly(tunFd)
            return false
        }
        activeTunFd = tunFd
        val started = try {
            TProxyStartService(configPath, tunFd.fd)
        } catch (t: Throwable) {
            Log.e(TAG, "TProxyStartService threw", t)
            false
        }
        if (!started) {
            Log.e(TAG, "TProxyStartService returned false")
            activeTunFd = null
            closeQuietly(tunFd)
            return false
        }
        return true
    }

    @Synchronized
    fun stop() {
        val fd = activeTunFd ?: return
        activeTunFd = null
        if (libraryLoaded) {
            try {
                TProxyStopService()
            } catch (t: Throwable) {
                Log.e(TAG, "TProxyStopService threw", t)
            }
        }
        closeQuietly(fd)
    }

    fun isRunning(): Boolean {
        if (!libraryLoaded) return false
        return try {
            TProxyIsRunning()
        } catch (t: Throwable) {
            Log.e(TAG, "TProxyIsRunning threw", t)
            false
        }
    }

    /** Returns [txPackets, txBytes, rxPackets, rxBytes] or null when unavailable. */
    fun stats(): LongArray? {
        if (!libraryLoaded) return null
        return try {
            TProxyGetStats()
        } catch (t: Throwable) {
            Log.e(TAG, "TProxyGetStats threw", t)
            null
        }
    }

    private fun closeQuietly(fd: ParcelFileDescriptor) {
        try {
            fd.close()
        } catch (_: Throwable) {
        }
    }
}
