plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}
android {
    namespace = "com.encore.satellite"
    compileSdk = 36
    defaultConfig {
        // Development identity: not a final release/coexistence decision.
        applicationId = "com.encore.satellite.incubator.dev"
        minSdk = 26
        targetSdk = 35
        versionCode = 2
        versionName = "0.1.1-incubator-dev"
        // Match the Go engine; transitive AndroidX libraries also ship x86 binaries.
        ndk { abiFilters += setOf("arm64-v8a", "armeabi-v7a") }
    }
    sourceSets {
        getByName("main") {
            kotlin.directories += "../../../platforms/android/src/main/java"
            res.srcDir("../../../platforms/android/src/main/res")
            assets.srcDir("../../../licenses")
        }
        getByName("test") {
            kotlin.directories += "../../../platforms/android/src/test/java"
        }
    }
    buildFeatures { compose = true; buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_21
        targetCompatibility = JavaVersion.VERSION_21
    }
    testOptions { unitTests.isIncludeAndroidResources = true }
    packaging { resources.excludes += setOf("META-INF/AL2.0", "META-INF/LGPL2.1") }
}
dependencies {
    implementation(files("../../../build/android/encore.aar"))
    implementation(project(":player"))
    implementation(platform("androidx.compose:compose-bom:2025.04.01"))
    implementation("androidx.activity:activity-compose:1.10.1")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.8.0")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.robolectric:robolectric:4.16")
    testImplementation("io.github.takahirom.roborazzi:roborazzi:1.79.0")
    testImplementation("androidx.compose.ui:ui-test-junit4")
    debugImplementation("androidx.compose.ui:ui-test-manifest")
}
