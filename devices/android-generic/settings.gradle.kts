pluginManagement {
    repositories { google(); mavenCentral(); gradlePluginPortal() }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories { google(); mavenCentral() }
}
rootProject.name = "EncoreIncubator"
include(":app", ":player", ":shared")
val vendor = file("../../build/android/sendspindroid/android")
project(":player").projectDir = vendor.resolve("player")
project(":shared").projectDir = vendor.resolve("shared")
