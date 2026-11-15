import java.net.URL

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val ciRunNumber = System.getenv("GITHUB_RUN_NUMBER")?.toIntOrNull() ?: 1

android {
    namespace = "app.mozillavpnlite"
    compileSdk = 35
    ndkVersion = "27.0.12077973"

    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }

    // Two release APKs: a small arm64-v8a build for modern 64-bit phones
    // (Pixel and almost everything recent) and a universal build carrying
    // every ABI from ndk.abiFilters for anything else.
    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a")
            isUniversalApk = true
        }
    }

    externalNativeBuild {
        ndkBuild {
            // Vendored hev-socks5-tunnel (see android/third_party/README.md).
            // Deliberately NOT ../hev-socks5-tunnel: that path is where the
            // legacy CI fetch step drops a fresh armanlife clone.
            path = file("../third_party/hev-socks5-tunnel/Android.mk")
        }
    }

    defaultConfig {
        applicationId = "app.mozillavpnlite"
        minSdk = 24
        targetSdk = 35
        versionCode = 100 + ciRunNumber
        versionName = "1.0.$ciRunNumber"

        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a")
        }

        externalNativeBuild {
            ndkBuild {
                targets += "hev-socks5-tunnel"
                arguments += listOf("REV_ID=2.19.3", "APP_SUPPORT_FLEXIBLE_PAGE_SIZES=true")
                cFlags += listOf("-O3", "-DPKGNAME=app/mozillavpnlite", "-DCLSNAME=Tun2Socks")
            }
        }
    }

    signingConfigs {
        create("releaseFixed") {
            val ksFile = file("release.p12")
            if (ksFile.exists()) {
                storeFile = ksFile
                storePassword = "android"
                keyAlias = "mozillavpnlite"
                keyPassword = "android"
                storeType = "PKCS12"
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            isShrinkResources = false
            val ksFile = file("release.p12")
            signingConfig = if (ksFile.exists()) {
                signingConfigs.getByName("releaseFixed")
            } else {
                signingConfigs.getByName("debug")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
    }
}

dependencies {
    implementation(files("libs/core.aar"))
    implementation(platform("androidx.compose:compose-bom:2024.10.01"))
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
}

// Design «Прибор» fonts are binaries, so they are not stored in the repo:
// they are fetched into assets/fonts before every build (and gitignored).
// Display: Russo One (Cyrillic native). Data: IBM Plex Mono.
// A failed fetch never breaks the build — the app falls back to system
// fonts at runtime (see PriborFonts in MainActivity.kt).
val designFontFiles = listOf(
    "RussoOne-Regular.ttf" to "https://raw.githubusercontent.com/google/fonts/main/ofl/russoone/RussoOne-Regular.ttf",
    "IBMPlexMono-Medium.ttf" to "https://raw.githubusercontent.com/google/fonts/main/ofl/ibmplexmono/IBMPlexMono-Medium.ttf",
    "IBMPlexMono-SemiBold.ttf" to "https://raw.githubusercontent.com/google/fonts/main/ofl/ibmplexmono/IBMPlexMono-SemiBold.ttf",
    "IBMPlexMono-Bold.ttf" to "https://raw.githubusercontent.com/google/fonts/main/ofl/ibmplexmono/IBMPlexMono-Bold.ttf"
)

tasks.register("fetchDesignFonts") {
    val fontsDir = layout.projectDirectory.dir("src/main/assets/fonts")
    outputs.dir(fontsDir)
    doLast {
        designFontFiles.forEach { (name, url) ->
            val out = fontsDir.file(name).asFile
            if (!out.exists() || out.length() == 0L) {
                out.parentFile.mkdirs()
                try {
                    URL(url).openStream().use { input ->
                        out.outputStream().use { output -> input.copyTo(output) }
                    }
                    logger.lifecycle("Fetched design font $name (${out.length()} bytes)")
                } catch (e: Exception) {
                    logger.warn("Design font $name could not be fetched (${e.message}); runtime fallback will be used")
                }
            }
        }
    }
}

tasks.named("preBuild") {
    dependsOn("fetchDesignFonts")
}
