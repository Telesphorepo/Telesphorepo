plugins {
    `java-gradle-plugin`
    id("com.gradle.plugin-publish") version "2.0.0"
}

group = "com.telesphorepo"

tasks.withType<JavaCompile>().configureEach {
    options.release = 17
    options.encoding = "UTF-8"
}

gradlePlugin {
    website = "https://github.com/Telesphorepo/Telesphorepo/tree/main/clients/gradle"
    vcsUrl = "https://github.com/Telesphorepo/Telesphorepo"
    plugins {
        create("telesphorepo") {
            id = "com.telesphorepo"
            implementationClass = "com.telesphorepo.gradle.TelesphorepoPlugin"
            displayName = "Telesphorepo"
            description = "Publish to a Telesphorepo artifact repository with short-lived, per-publish " +
                "credentials minted from an enrolled device key or a CI OIDC token (GitHub Actions, " +
                "Jenkins). No long-lived passwords in builds or CI."
            tags = listOf("publishing", "maven", "repository", "credentials", "security", "oidc")
        }
    }
}

publishing {
    publications.withType<MavenPublication>().configureEach {
        pom {
            name = "Telesphorepo Gradle plugin"
            description = "Gradle settings plugin that publishes to a Telesphorepo server with short-lived publish sessions."
            url = "https://github.com/Telesphorepo/Telesphorepo/tree/main/clients/gradle"
            licenses {
                license {
                    name = "MIT"
                    url = "https://opensource.org/license/mit"
                }
            }
            developers {
                developer {
                    id = "telesphoreo"
                    name = "Telesphoreo"
                    url = "https://github.com/Telesphorepo"
                }
            }
            scm {
                connection = "scm:git:https://github.com/Telesphorepo/Telesphorepo.git"
                developerConnection = "scm:git:git@github.com:Telesphorepo/Telesphorepo.git"
                url = "https://github.com/Telesphorepo/Telesphorepo"
            }
        }
    }
    repositories {
        // The release workflow sets these variables. The repository publishes the plugin and its
        // marker artifact (com.telesphorepo:com.telesphorepo.gradle.plugin).
        val releaseUrl = providers.environmentVariable("NEXUS_MAVEN_URL")
        if (releaseUrl.isPresent) {
            maven {
                name = "release"
                url = uri(releaseUrl.get())
                credentials {
                    username = providers.environmentVariable("NEXUS_USERNAME").orNull
                    password = providers.environmentVariable("NEXUS_PASSWORD").orNull
                }
            }
        }
    }
}

// A release has a final version, which the workflow takes from the tag.
// The task name ends in "ToReleaseRepository"; the repository itself is not set yet when
// configureEach runs.
tasks.withType<PublishToMavenRepository>().configureEach {
    if (name.endsWith("ToReleaseRepository")) {
        val publishVersion = project.version.toString()
        doFirst {
            check(!publishVersion.endsWith("-SNAPSHOT")) {
                "Release builds need a final version. Pass -Pversion=<version>."
            }
        }
    }
}