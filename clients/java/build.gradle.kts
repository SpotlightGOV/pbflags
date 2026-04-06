import com.google.protobuf.gradle.id

plugins {
    java
    `java-library`
    `maven-publish`
    signing
    id("com.google.protobuf") version "0.9.6"
    id("io.github.gradle-nexus.publish-plugin") version "2.0.0"
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

val protocVersion = "4.29.3"
val grpcVersion = "1.80.0"

dependencies {
    api("com.google.protobuf:protobuf-java:$protocVersion")
    api("io.grpc:grpc-stub:$grpcVersion")
    api("io.grpc:grpc-protobuf:$grpcVersion")

    implementation("io.grpc:grpc-netty-shaded:$grpcVersion")
    implementation("org.slf4j:slf4j-api:2.0.17")
    compileOnly("javax.annotation:javax.annotation-api:1.3.2")

    testImplementation("org.junit.jupiter:junit-jupiter:5.14.3")
    testImplementation("io.grpc:grpc-inprocess:$grpcVersion")
    testImplementation("io.grpc:grpc-testing:$grpcVersion")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.test {
    useJUnitPlatform()
}

protobuf {
    protoc {
        artifact = "com.google.protobuf:protoc:$protocVersion"
    }
    plugins {
        id("grpc") {
            artifact = "io.grpc:protoc-gen-grpc-java:$grpcVersion"
        }
    }
    generateProtoTasks {
        all().forEach { task ->
            task.plugins {
                id("grpc")
            }
        }
    }
}

sourceSets {
    main {
        proto {
            srcDir("../../proto")
        }
    }
}

publishing {
    publications {
        create<MavenPublication>("mavenJava") {
            from(components["java"])
            pom {
                name.set("pbflags-java")
                description.set("Java client for pbflags feature flag evaluator")
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

nexusPublishing {
    repositories {
        sonatype {
            nexusUrl.set(uri("https://ossrh-staging-api.central.sonatype.com/service/local/"))
            snapshotRepositoryUrl.set(uri("https://central.sonatype.com/repository/maven-snapshots/"))
            username.set(System.getenv("OSSRH_USERNAME") ?: "")
            password.set(System.getenv("OSSRH_TOKEN") ?: "")
        }
    }
}

signing {
    val signingKey = System.getenv("GPG_PRIVATE_KEY") ?: ""
    val signingPassword = (System.getenv("GPG_PASSPHRASE") ?: "").trim()
    useInMemoryPgpKeys(signingKey, signingPassword)
    sign(publishing.publications["mavenJava"])
}
