plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.android) apply false // only pins the Kotlin version for AGP's built-in Kotlin
    alias(libs.plugins.kotlin.compose) apply false
}
