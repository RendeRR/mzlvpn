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

    externalNativeBuild {
        ndkBuild {
            path = file("../hev-socks5-tunnel/Android.mk")
        }
    }

    defaultConfig {
        applicationId = "app.mozillavpnlite"
        minSdk = 24
        targetSdk = 35
        versionCode = 100 + ciRunNumber
        versionName = "1.0.$ciRunNumber-lite"

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
