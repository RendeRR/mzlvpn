# Keep gomobile JNI bridge classes and methods
-keep class go.** { *; }
-keep class mobile.** { *; }
-dontwarn go.**
-dontwarn mobile.**

# Keep hev-socks5-tunnel JNI target class and native methods (RegisterNatives)
-keep class app.mozillavpnlite.Tun2Socks {
    *;
}

# Keep WebView JavascriptInterface methods for FxA WebChannel bridge
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}
-keepattributes JavascriptInterface,*Annotation*
