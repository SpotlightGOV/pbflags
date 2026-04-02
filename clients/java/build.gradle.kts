import com.google.protobuf.gradle.id

plugins {
    java
    `java-library`
    `maven-publish`
    id("com.google.protobuf") version "0.9.4"
}

group = "org.spotlightgov.pbflags"
version = "0.1.0"

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
val grpcVersion = "1.70.0"

dependencies {
    api("com.google.protobuf:protobuf-java:$protocVersion")
    api("io.grpc:grpc-stub:$grpcVersion")
    api("io.grpc:grpc-protobuf:$grpcVersion")

    implementation("io.grpc:grpc-netty-shaded:$grpcVersion")
    implementation("org.slf4j:slf4j-api:2.0.16")
    compileOnly("javax.annotation:javax.annotation-api:1.3.2")
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
                        name.set("Apache License 2.0")
                        url.set("https://www.apache.org/licenses/LICENSE-2.0")
                    }
                }
            }
        }
    }
}
