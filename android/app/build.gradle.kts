import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
}

// Release signing from android/local.properties (git-ignored, never committed):
// release.storeFile (relative to android/, e.g. keystore/relay-release.jks; git-ignored),
// release.storePassword, release.keyAlias, release.keyPassword.
// Without them the release build is unsigned (CI).
val localProps = Properties().apply {
    rootProject.file("local.properties").takeIf { it.isFile }?.inputStream()?.use { load(it) }
}
val releaseStore: String? = localProps.getProperty("release.storeFile")

android {
    namespace = "com.zyndata.crossstream.relay"
    compileSdk = 37

    defaultConfig {
        applicationId = "com.zyndata.crossstream.relay"
        minSdk = 29
        targetSdk = 37
        // Release builds (deploy.yml) pass the tag: -PxsVersionName=1.2.3 -PxsVersionCode=10203.
        versionCode = (findProperty("xsVersionCode") as String?)?.toInt() ?: 2
        // Local builds are "0.0.0-dev": never offered an update (a release APK is signed with
        // another key and could not replace them anyway).
        versionName = (findProperty("xsVersionName") as String?) ?: "0.0.0-dev"
    }

    signingConfigs {
        if (releaseStore != null) {
            create("release") {
                storeFile = rootProject.file(releaseStore) // relative to android/
                storePassword = localProps.getProperty("release.storePassword")
                keyAlias = localProps.getProperty("release.keyAlias")
                keyPassword = localProps.getProperty("release.keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (releaseStore != null) signingConfig = signingConfigs.getByName("release")
        }
    }
    buildFeatures {
        compose = true
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    testOptions {
        unitTests.isReturnDefaultValues = true
    }
}

dependencies {
    // Relay core built by `make aar` (gomobile, ../mobile).
    implementation(files("libs/xscore.aar"))

    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.service)
    implementation(libs.androidx.datastore.preferences)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    implementation(libs.compose.ui.tooling.preview)
    debugImplementation(libs.compose.ui.tooling)
    implementation(libs.camera.camera2)
    implementation(libs.camera.lifecycle)
    implementation(libs.camera.view)
    implementation(libs.camera.mlkit.vision)
    implementation(libs.mlkit.barcode)

    testImplementation(libs.junit)
    testImplementation(libs.org.json) // android.jar's org.json is a stub in JVM unit tests
}
