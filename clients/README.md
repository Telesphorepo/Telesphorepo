# Telesphorepo clients

A write to a Telesphorepo repository needs a **publish session**: a short-lived token that the
server gives for one repository. Passwords and read tokens can only read. These clients get a
session for you.

| Client | Directory | Setup |
|---|---|---|
| Gradle settings plugin `com.telesphorepo` | [`gradle/`](gradle/README.md) | `./gradlew telesphorepoLogin` once |
| Maven extension | [`maven/`](maven/) | `mvn telesphorepo:login` once |

The clients are MIT-licensed (SPDX: `MIT`, see [`LICENSE`](LICENSE)). The server is AGPL-3.0.

In CI, the clients use the CI's identity token (OIDC). An admin must add a **trust policy** on
the server's **Trust policies** page. The policy names the issuer, the claims and the
repositories.

## CI without a client

Get a session with two `curl` calls, then use its token as the HTTP Basic password (any
username). The examples use `jq`.

### GitHub Actions

The job needs `permissions: id-token: write`.

```sh
OIDC=$(curl -sSf -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
  "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https://repo.acme.com" | jq -r .value)
SESSION=$(curl -sSf https://repo.acme.com/api/publish/sessions/oidc \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$OIDC\",\"repo\":\"releases\"}")
TOKEN=$(echo "$SESSION" | jq -r .token)

curl -sSf -u "ci:$TOKEN" -T app-1.0.jar \
  https://repo.acme.com/maven/releases/com/acme/app/1.0/app-1.0.jar

# End the session when the publish is done.
curl -sSf -X DELETE -u "ci:$TOKEN" \
  "https://repo.acme.com/api/publish/sessions/$(echo "$SESSION" | jq -r .id)"
```

### Jenkins

Install the **OpenID Connect Provider** plugin and add an "OpenID Connect id token" credential
with the audience `https://repo.acme.com`. The trust policy issuer is `<jenkins url>/oidc`.

```groovy
withCredentials([string(credentialsId: 'telesphorepo-oidc', variable: 'OIDC')]) {
  sh '''
    SESSION=$(curl -sSf https://repo.acme.com/api/publish/sessions/oidc \
      -H 'Content-Type: application/json' \
      -d "{\\"token\\":\\"$OIDC\\",\\"repo\\":\\"releases\\"}")
    TOKEN=$(echo "$SESSION" | jq -r .token)
    curl -sSf -u "ci:$TOKEN" -T app-1.0.jar \
      https://repo.acme.com/maven/releases/com/acme/app/1.0/app-1.0.jar
  '''
}
```

With the Gradle plugin, set `oidcTokenEnv = "OIDC"` instead and run the build inside
`withCredentials`.

A session ends by itself after `publish.session_ttl` (15 minutes by default).


## Get the clients from the Nexus repository

A `clients-v<version>` tag publishes both clients to the owner's Nexus release repository. The
Gradle Plugin Portal and Maven Central do not have them yet. Until they do, add the Nexus
repository (URL: `NEXUS_MAVEN_URL`, for example
`https://nexus.example.com/repository/maven-releases/`) to your build.

Gradle, in `settings.gradle.kts`, before the `plugins` block:

```kotlin
pluginManagement {
    repositories {
        maven {
            url = uri("https://nexus.example.com/repository/maven-releases/")
            // credentials { ... } only when the repository needs a login to read
        }
        gradlePluginPortal()
    }
}
```

Maven, for the extension: add the repository to `~/.m2/settings.xml` in an active profile, and
declare the extension in `.mvn/extensions.xml` (see [`maven/`](maven/README.md)). A core
extension resolves from the repositories of `settings.xml`, not from `pom.xml`:

```xml
<profiles>
  <profile>
    <id>telesphorepo-clients</id>
    <repositories>
      <repository><id>telesphorepo-clients</id><url>https://nexus.example.com/repository/maven-releases/</url></repository>
    </repositories>
    <pluginRepositories>
      <pluginRepository><id>telesphorepo-clients</id><url>https://nexus.example.com/repository/maven-releases/</url></pluginRepository>
    </pluginRepositories>
  </profile>
</profiles>
<activeProfiles><activeProfile>telesphorepo-clients</activeProfile></activeProfiles>
```

The same `<pluginRepositories>` block works in `pom.xml` for the `telesphorepo:login` plugin.

## Release (maintainers)

`.github/workflows/release-clients.yml` runs on a `clients-v<version>` tag. The version comes
from the tag and must be final (no `-SNAPSHOT`). Set these GitHub secrets first:

| Secret | Value |
|---|---|
| `NEXUS_MAVEN_URL` | Full URL of the hosted Maven release repository |
| `NEXUS_USERNAME` | User that may deploy to it |
| `NEXUS_PASSWORD` | Password or token of that user |

The Gradle job publishes the plugin and its marker with the `release` repository of
`gradle/build.gradle.kts`. The Maven job runs `mvn deploy` with `-DaltDeploymentRepository`.
The workflow has the Plugin Portal and Maven Central jobs as comments. Enable them after
namespace verification. They use `GRADLE_PUBLISH_KEY`, `GRADLE_PUBLISH_SECRET`,
`MAVEN_CENTRAL_USERNAME`, `MAVEN_CENTRAL_PASSWORD`, `MAVEN_GPG_PRIVATE_KEY` and
`MAVEN_GPG_PASSPHRASE`.