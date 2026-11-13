pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "tvt-android"
include(":app")
include(":shell")
project(":shell").projectDir = file(providers.gradleProperty("shellDir").orElse("../../lib/android/shell").get())
