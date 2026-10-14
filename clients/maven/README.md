# Telesphorepo Maven extension

The extension gets a publish session from Telesphorepo before `maven-deploy-plugin` uploads, and
closes it when the build ends. `settings.xml` needs no password for the server. Requires Maven
3.9 and Java 17.

## Set up a project

Declare the extension in `.mvn/extensions.xml`:

```xml
<extensions>
  <extension>
    <groupId>com.telesphorepo</groupId>
    <artifactId>telesphorepo-maven-extension</artifactId>
    <version>0.1.0</version>
  </extension>
</extensions>
```

Name the server in `pom.xml` and deploy to it as usual:

```xml
<properties>
  <telesphorepo.url>https://repo.acme.com</telesphorepo.url>
</properties>
<distributionManagement>
  <repository><id>telesphorepo</id><url>https://repo.acme.com/maven/releases</url></repository>
  <snapshotRepository><id>telesphorepo</id><url>https://repo.acme.com/maven/snapshots</url></snapshotRepository>
</distributionManagement>
```

`-Dtelesphorepo.url=...` and the environment variable `TELESPHOREPO_URL` also work. The
extension handles every deployment repository whose URL is `<telesphorepo.url>/maven/<repo>`,
`-DaltDeploymentRepository` and `deploy:deploy-file` included.

## Enroll your machine (once)

Add the plugin group to `~/.m2/settings.xml` so that `telesphorepo:` resolves:

```xml
<pluginGroups>
  <pluginGroup>com.telesphorepo</pluginGroup>
</pluginGroups>
```

Then run:

```sh
mvn telesphorepo:login -Dtelesphorepo.url=https://repo.acme.com
```

Without the plugin group, run
`mvn com.telesphorepo:telesphorepo-maven-plugin:login -Dtelesphorepo.url=...`.
Open the URL it prints, sign in with your password and second factor, and approve the code.
The key goes to `~/.telesphorepo/<host>/device.json`, and only you can read it. The Gradle
plugin uses the same file. `-Dtelesphorepo.deviceName=...` sets the name the device list shows.

## Publish

```sh
mvn deploy
```

The extension proves who you are, in this order:

1. GitHub Actions: the job's identity token, when `ACTIONS_ID_TOKEN_REQUEST_URL` is set. The job
   needs `permissions: id-token: write`.
2. Other CI, such as Jenkins: the identity token in the environment variable that the property
   `telesphorepo.oidcTokenEnv` names. When that variable is empty, the extension uses the device
   key.
3. The device key from `mvn telesphorepo:login`.

CI needs an admin **trust policy** on the server. With no proof, `mvn deploy` stops before the
build and tells you to run `mvn telesphorepo:login`.

The session lasts `publish.session_ttl` (15 minutes by default). The extension gets the
session when the deploy goal of the first module starts. With `-DdeployAtEnd=true`,
`maven-deploy-plugin` keeps each module's credentials until the last module uploads, so the
whole reactor must finish within that time.

## Build the clients

```sh
mvn -f clients/maven install
```

`mvn deploy -DaltDeploymentRepository=release::<url>` publishes to a Maven repository such as
Nexus. The release workflow does this. `mvn -Prelease deploy` signs the artifacts and publishes
them to Maven Central. It needs a `central` server in `settings.xml` and a GPG key. To resolve
the extension from Nexus, see [`../README.md`](../README.md).

License: MIT (SPDX: `MIT`).
