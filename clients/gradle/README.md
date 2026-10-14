# Telesphorepo Gradle plugin

A settings plugin that publishes to a Telesphorepo server without a stored secret. When a task
publishes to a Maven repository on the server, the plugin gets a publish session for that
repository. The session ends when the build ends.

## Use it

In `settings.gradle.kts`:

```kotlin
plugins {
    id("com.telesphorepo") version "<version>"
}

telesphorepo {
    url = "https://repo.acme.com"
}
```

In `build.gradle.kts`, declare the repository as usual, without credentials:

```kotlin
publishing {
    repositories {
        maven {
            name = "acme"
            url = uri("https://repo.acme.com/maven/releases")
        }
    }
}
```

This works for libraries (`maven-publish`) and for Gradle plugins (`java-gradle-plugin` with
`maven-publish`).

## Proof of identity

For each repository, the plugin uses the first proof that it finds:

1. **GitHub Actions.** The workflow needs `permissions: id-token: write`, and an admin adds a
   trust policy for the repository on the server.
2. **Another CI with OIDC, such as Jenkins.** Set `oidcTokenEnv` to the name of the environment
   variable that holds the identity token:

   ```kotlin
   telesphorepo {
       url = "https://repo.acme.com"
       oidcTokenEnv = "OIDC_TOKEN"
   }
   ```

3. **This machine's device key.** Enroll once:

   ```sh
   ./gradlew telesphorepoLogin
   ```

   The task prints a line like `Open https://repo.acme.com/#/device?code=ABCD-EFGH and approve code ABCD-EFGH`.
   Open the URL in any browser, sign in, and approve the code. The task then saves the key in
   `~/.telesphorepo/<host>/device.json`, which only you can read. The device stays valid for 90
   days by default. You can revoke it on the server's **Devices** page.

With no proof, a publish fails with `run ./gradlew telesphorepoLogin`.

## Notes

- The plugin sets credentials only on repositories under `<url>/maven/`. Other repositories are
  not changed.
- A build that publishes to the server does not use the configuration cache, because the plugin
  sets the credentials when the task runs. Other builds and other publish tasks are not changed.

## Build

```sh
gradle build                 # in clients/gradle
gradle publishToMavenLocal   # try a local build in another project through mavenLocal()
```

## License

MIT (SPDX: `MIT`).
