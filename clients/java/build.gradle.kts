plugins {
    java
    `java-library`
    `maven-publish`
}

group = "io.pbflags"
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

dependencies {
    api("com.google.protobuf:protobuf-java:4.29.3")
    api("io.grpc:grpc-stub:1.70.0")
    api("io.grpc:grpc-protobuf:1.70.0")

    implementation("io.grpc:grpc-netty-shaded:1.70.0")
    implementation("org.slf4j:slf4j-api:2.0.16")
    compileOnly("javax.annotation:javax.annotation-api:1.3.2")
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
