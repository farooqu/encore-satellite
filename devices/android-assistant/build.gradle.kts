plugins { id("com.android.application") version "9.2.1" }

val sharedVoice = tasks.register<Sync>("prepareVoiceHost") {
    from("../../platforms/android/src/main/java") { include("**/AudioIo.kt", "**/VoiceReference.kt") }
    into(layout.buildDirectory.dir("generated/voiceHost"))
}
val aec = file("../../build/android/aec")
android {
    namespace = "com.encore.personal"
    compileSdk = 36
    defaultConfig {
        applicationId = "com.encore.personal"
        minSdk = 26
        targetSdk = 35
        versionCode = providers.environmentVariable("ENCORE_VERSION_CODE").orElse("1").get().toInt()
        versionName = providers.environmentVariable("ENCORE_VERSION_NAME").orElse("0.1.0-prototype").get()
        ndk { abiFilters += setOf("arm64-v8a", "armeabi-v7a") }
    }
    buildTypes { getByName("debug") { applicationIdSuffix = ".dev" } }
    sourceSets.getByName("main").apply {
        kotlin.directories += sharedVoice.map { it.destinationDir.path }.get()
        jniLibs.directories += aec.resolve("jniLibs").path
        assets.directories += aec.resolve("assets").path
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_21
        targetCompatibility = JavaVersion.VERSION_21
    }
    testOptions { unitTests.isIncludeAndroidResources = true }
}
tasks.named("preBuild") { dependsOn(sharedVoice) }
abstract class VoiceNotices : Sync() {
    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty
    init { into(outputDirectory) }
}
val voiceNotices = tasks.register<VoiceNotices>("prepareVoiceNotices") {
    from("../../LICENSE", "../../licenses/Apache-2.0.txt", "../../licenses/Mixednet-NOTICE.txt",
        "../../licenses/Techo5-MIT.txt", "../../licenses/Techo5-NOTICE.txt", "../../licenses/Go-Dependencies-NOTICE.txt")
    outputDirectory.set(layout.buildDirectory.dir("generated/voiceNotices"))
}
androidComponents.onVariants { variant ->
    variant.sources.assets?.addGeneratedSourceDirectory(voiceNotices) { it.outputDirectory }
}
dependencies {
    implementation(files("../../build/android/encore-assistant.aar"))
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.robolectric:robolectric:4.16")
}
