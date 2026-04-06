plugins {
    java
    `java-library`
    `maven-publish`
    signing
}

group = "org.spotlightgov.pbflags"
version = providers.environmentVariable("RELEASE_VERSION").map { it.trim().removePrefix("v") }.orElse(
    providers.exec {
        commandLine("git", "describe", "--tags", "--abbrev=0")
    }.standardOutput.asText.map { it.trim().removePrefix("v") }
).getOrElse("0.0.0-SNAPSHOT")

java {
    sourceCompatibility = JavaVersion.VERSION_21
    targetCompatibility = JavaVersion.VERSION_21
    withSourcesJar()
    withJavadocJar()
}

repositories {
    mavenCentral()
}

dependencies {
    api(project(":"))
    compileOnly("org.junit.jupiter:junit-jupiter-api:5.14.3")
    compileOnly("javax.annotation:javax.annotation-api:1.3.2")
}

publishing {
    publications {
        create<MavenPublication>("mavenJava") {
            artifactId = "pbflags-java-testing"
            from(components["java"])
            pom {
                name.set("pbflags-java-testing")
                description.set("Test utilities for pbflags feature flags (InMemoryFlagEvaluator, JUnit 5 extension)")
                url.set("https://github.com/SpotlightGOV/pbflags")
                licenses {
                    license {
                        name.set("MIT License")
                        url.set("https://opensource.org/licenses/MIT")
                    }
                }
                developers {
                    developer {
                        id.set("spotlightgov")
                        name.set("SpotlightGOV")
                        url.set("https://github.com/SpotlightGOV")
                    }
                }
                scm {
                    connection.set("scm:git:git://github.com/SpotlightGOV/pbflags.git")
                    developerConnection.set("scm:git:ssh://github.com/SpotlightGOV/pbflags.git")
                    url.set("https://github.com/SpotlightGOV/pbflags")
                }
            }
        }
    }
}

signing {
    val signingKey = System.getenv("GPG_PRIVATE_KEY") ?: ""
    val signingPassword = (System.getenv("GPG_PASSPHRASE") ?: "").trim()
    useInMemoryPgpKeys(signingKey, signingPassword)
    sign(publishing.publications["mavenJava"])
}
