package com.telesphorepo.gradle;

import org.gradle.api.provider.Property;

/**
 * The {@code telesphorepo { }} block in the settings script.
 *
 * <pre>
 * telesphorepo {
 *     url = "https://repo.acme.com"
 *     oidcTokenEnv = "OIDC_TOKEN" // optional: a CI identity token, e.g. from Jenkins
 * }
 * </pre>
 */
public abstract class TelesphorepoExtension {
    /** The server's base URL. Maven repositories under {@code <url>/maven/} get publish sessions. */
    public abstract Property<String> getUrl();

    /** The name of an environment variable that holds a CI identity token (OIDC). Optional. */
    public abstract Property<String> getOidcTokenEnv();
}
