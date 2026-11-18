import java.util.Base64
import groovy.json.JsonSlurper

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}
fun env(name: String) = System.getenv(name).orEmpty()
val coreVersion = (JsonSlurper().parse(file("../../../release/targets.json")) as Map<*, *>)["core_version"] as String
fun quoted(value: String) = "\"" + value.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n").replace("\r", "\\r") + "\""
abstract class EncoreLicenseAssets : Sync() {
    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty
    init { into(outputDirectory) }
}
val encoreLicenseAssets = tasks.register<EncoreLicenseAssets>("prepareEncoreLicenseAssets") {
    from("../../../LICENSE")
    outputDirectory.set(layout.buildDirectory.dir("generated/encoreLicenseAssets"))
}
gradle.taskGraph.whenReady {
  if (allTasks.any { it.project == project && it.name.contains("release", ignoreCase = true) }) {
    listOf("ENCORE_KEYSTORE", "ENCORE_STORE_PASSWORD", "ENCORE_KEY_ALIAS", "ENCORE_KEY_PASSWORD", "ENCORE_VERSION_CODE", "ENCORE_VERSION_NAME", "ENCORE_UPDATE_BASE_URL", "ENCORE_UPDATE_PUBLIC_KEY").forEach {
        require(env(it).isNotBlank()) { "$it is required for a signed release" }
    }
    require(env("ENCORE_VERSION_CODE").toInt() in 3..2100000000)
    require(env("ENCORE_UPDATE_BASE_URL").startsWith("https://"))
    require(Base64.getDecoder().decode(env("ENCORE_UPDATE_PUBLIC_KEY")).size == 32)
  }
}
android {
    namespace = "com.encore.satellite"
    compileSdk = 36
    // AGP encrypts this optional SDK inventory with fresh randomness per build.
    // Corresponding dependencies remain in the retained source kit instead.
    dependenciesInfo { includeInApk = false }
    defaultConfig {
        // Signed dev/stable releases share an identity separate from debug data.
        applicationId = "com.encore.satellite.incubator"
        minSdk = 26
        targetSdk = 35
        versionCode = env("ENCORE_VERSION_CODE").ifBlank { "3" }.toInt()
        versionName = env("ENCORE_VERSION_NAME").ifBlank { "0.1.2-incubator-dev" }
        buildConfigField("String", "UPDATE_BASE_URL", quoted(env("ENCORE_UPDATE_BASE_URL")))
        buildConfigField("String", "UPDATE_PUBLIC_KEY", quoted(env("ENCORE_UPDATE_PUBLIC_KEY")))
        buildConfigField("String", "CORE_VERSION", quoted(coreVersion))
        manifestPlaceholders["coreVersion"] = coreVersion
        // Match the Go engine; transitive AndroidX libraries also ship x86 binaries.
        ndk { abiFilters += setOf("arm64-v8a", "armeabi-v7a") }
    }
    signingConfigs {
        create("release") {
            if (env("ENCORE_KEYSTORE").isNotBlank()) {
                storeFile = file(env("ENCORE_KEYSTORE"))
                storePassword = env("ENCORE_STORE_PASSWORD")
                keyAlias = env("ENCORE_KEY_ALIAS")
                keyPassword = env("ENCORE_KEY_PASSWORD")
            }
        }
    }
    buildTypes {
        getByName("debug") { applicationIdSuffix = ".dev" }
        getByName("release") { signingConfig = signingConfigs.getByName("release") }
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
androidComponents.onVariants { variant ->
    variant.sources.assets?.addGeneratedSourceDirectory(encoreLicenseAssets) { it.outputDirectory }
}
dependencies {
    implementation("androidx.core:core:1.15.0")
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
