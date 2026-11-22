import java.util.Base64
import java.net.URI
import groovy.json.JsonSlurper
import org.gradle.api.artifacts.component.ModuleComponentIdentifier
import org.gradle.api.attributes.Category
import org.gradle.api.attributes.DocsType

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}
fun env(name: String) = System.getenv(name).orEmpty()
val aecOutput = file("../../../build/android/aec")
require(listOf("arm64-v8a", "armeabi-v7a").all { aecOutput.resolve("jniLibs/$it/libencore_aec.so").isFile }) {
    "Run devices/android-generic/build-android to build AEC3 before packaging the APK"
}
val coreVersion = env("ENCORE_CORE_VERSION").ifBlank {
    (JsonSlurper().parse(file("../../../release/targets.json")) as Map<*, *>)["core_version"] as String
}
require(coreVersion.matches(Regex("[0-9A-Za-z.+-]+"))) { "Invalid core version" }
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
    val feed = URI(env("ENCORE_UPDATE_BASE_URL"))
    require(feed.scheme == "https" && feed.host != null && feed.userInfo == null && feed.query == null && feed.fragment == null) {
        "ENCORE_UPDATE_BASE_URL must be an HTTPS base URL without credentials, a query or a fragment"
    }
    require(Base64.getDecoder().decode(env("ENCORE_UPDATE_PUBLIC_KEY")).size == 32) { "ENCORE_UPDATE_PUBLIC_KEY must be a raw Ed25519 public key" }
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
        buildConfigField("String", "CORE_VERSION", "\"$coreVersion\"")
        manifestPlaceholders["coreVersion"] = coreVersion
        manifestPlaceholders["updateBaseUrl"] = env("ENCORE_UPDATE_BASE_URL")
        manifestPlaceholders["updatePublicKey"] = env("ENCORE_UPDATE_PUBLIC_KEY")
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
            res.srcDir("../../../platforms/android/voice-ui/res")
            assets.srcDir("../../../licenses")
            jniLibs.directories += aecOutput.resolve("jniLibs").path
            assets.directories += aecOutput.resolve("assets").path
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
tasks.register("collectReleaseDependencySources") {
    val runtime = configurations.named("releaseRuntimeClasspath")
    val destination = layout.buildDirectory.dir("release-dependency-sources")
    doLast {
        val binaries = runtime.get().incoming.artifactView {
            componentFilter { it is ModuleComponentIdentifier }
        }.artifacts.artifacts.map {
            it.id.componentIdentifier as ModuleComponentIdentifier
        }.toSet()
        val sources = runtime.get().incoming.artifactView {
            withVariantReselection()
            isLenient = true
            componentFilter { it is ModuleComponentIdentifier }
            attributes {
                attribute(Category.CATEGORY_ATTRIBUTE, objects.named(Category.DOCUMENTATION))
                attribute(DocsType.DOCS_TYPE_ATTRIBUTE, objects.named(DocsType.SOURCES))
            }
        }.artifacts.artifacts.groupBy { it.id.componentIdentifier }
        val dir = destination.get().asFile
        check(!dir.exists() || dir.deleteRecursively()) { "Could not clear previous dependency sources" }
        for (id in binaries) {
            val artifacts = sources[id].orEmpty()
            check(artifacts.isNotEmpty()) { "No published runtime dependency sources for ${id.displayName}" }
            for (artifact in artifacts) artifact.file.copyTo(dir.resolve("${id.group}/${id.module}/${id.version}/${artifact.file.name}"))
        }
    }
}
dependencies {
    implementation("androidx.core:core:1.15.0")
    implementation("androidx.webkit:webkit:1.12.1")
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
