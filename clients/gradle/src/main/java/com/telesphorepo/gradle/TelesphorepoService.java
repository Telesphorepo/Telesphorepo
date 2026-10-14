package com.telesphorepo.gradle;

import org.gradle.api.GradleException;
import org.gradle.api.logging.Logger;
import org.gradle.api.logging.Logging;
import org.gradle.api.provider.Property;
import org.gradle.api.services.BuildService;
import org.gradle.api.services.BuildServiceParameters;

import java.net.URI;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Mints one publish session per repository on first use and closes every session it minted when
 * the build ends. The proof is, in order: a GitHub Actions identity token, the identity token in
 * the environment variable named by {@code telesphorepo.oidcTokenEnv}, the enrolled device key.
 */
public abstract class TelesphorepoService implements BuildService<TelesphorepoService.Params>, AutoCloseable {
    /** The HTTP Basic username sent with a session token. The server ignores it. */
    static final String SESSION_USER = "telesphorepo";

    private static final Logger LOG = Logging.getLogger(TelesphorepoService.class);

    public interface Params extends BuildServiceParameters {
        Property<String> getUrl();

        Property<String> getOidcTokenEnv();
    }

    private static final class Session {
        final String id;
        final String token;

        Session(String id, String token) {
            this.id = id;
            this.token = token;
        }
    }

    private final Map<String, Session> sessions = new ConcurrentHashMap<>();
    private volatile Api api;

    private Api api() {
        Api a = api;
        if (a == null) {
            synchronized (this) {
                if (api == null) {
                    api = new Api(getParameters().getUrl().get());
                }
                a = api;
            }
        }
        return a;
    }

    /**
     * Returns the repository name when repo is a Maven repository on the configured server
     * ({@code <url>/maven/<name>/...}), or null for any other URL. The scheme is not compared.
     */
    String repoName(URI repo) {
        if (!getParameters().getUrl().isPresent()) {
            return null;
        }
        return repoName(api().base, repo);
    }

    /** Returns the repository name when repo is {@code <base>/maven/<name>/...}, or null. */
    static String repoName(URI base, URI repo) {
        if (repo == null || repo.getHost() == null || repo.getPath() == null) {
            return null;
        }
        if (!repo.getHost().equalsIgnoreCase(base.getHost()) || port(repo) != port(base)) {
            return null;
        }
        String prefix = (base.getPath() == null ? "" : base.getPath()) + "/maven/";
        if (!repo.getPath().startsWith(prefix)) {
            return null;
        }
        String rest = repo.getPath().substring(prefix.length());
        int slash = rest.indexOf('/');
        String name = slash < 0 ? rest : rest.substring(0, slash);
        return name.isEmpty() ? null : name;
    }

    private static int port(URI u) {
        if (u.getPort() != -1) {
            return u.getPort();
        }
        return "https".equalsIgnoreCase(u.getScheme()) ? 443 : 80;
    }

    /** Returns the session token for repo, minting a session on the first call in this build. */
    String token(String repo) {
        return sessions.computeIfAbsent(repo, this::mint).token;
    }

    private Session mint(String repo) {
        Api a = api();
        Map<String, Object> resp;
        String via;
        String oidc = ciToken(a);
        if (oidc != null) {
            via = "a CI identity token";
            Map<String, Object> body = new LinkedHashMap<>();
            body.put("token", oidc);
            body.put("repo", repo);
            resp = a.post("/api/publish/sessions/oidc", body);
        } else {
            DeviceKey device = DeviceKey.load(a.base);
            if (device == null) {
                throw new GradleException("No Telesphorepo credential to publish to " + a.base + "/maven/" + repo
                    + ": run ./gradlew telesphorepoLogin");
            }
            via = "device " + device.deviceId;
            resp = mintWithDevice(a, device, repo);
        }
        String id = Json.text(resp, "id");
        String token = Json.string(resp, "token");
        if (id == null || token == null) {
            throw new GradleException("The server's publish session answer has no id or token");
        }
        LOG.lifecycle("Telesphorepo: publish session {} for {} ({})", id, repo, via);
        return new Session(id, token);
    }

    private static Map<String, Object> mintWithDevice(Api a, DeviceKey device, String repo) {
        try {
            Map<String, Object> challenge = new LinkedHashMap<>();
            challenge.put("device_id", device.deviceId);
            String nonce = Json.string(a.post("/api/publish/challenge", challenge), "nonce");
            if (nonce == null) {
                throw new GradleException("The server's challenge answer has no nonce");
            }
            String msg = "telesphorepo publish v1\n" + nonce + "\n" + device.deviceId + "\n" + repo;
            Map<String, Object> body = new LinkedHashMap<>();
            body.put("device_id", device.deviceId);
            body.put("repo", repo);
            body.put("nonce", nonce);
            body.put("signature", device.sign(msg));
            return a.post("/api/publish/sessions", body);
        } catch (Api.ApiException e) {
            if (e.status == 401 || e.status == 404) {
                throw new GradleException("The server refused device " + device.deviceId + " (" + e.error
                    + "). It may be revoked or expired: run ./gradlew telesphorepoLogin", e);
            }
            throw e;
        }
    }

    /** Returns a CI identity token from GitHub Actions or from oidcTokenEnv, or null when there is none. */
    private String ciToken(Api a) {
        String reqUrl = System.getenv("ACTIONS_ID_TOKEN_REQUEST_URL");
        if (reqUrl != null && !reqUrl.isEmpty()) {
            String reqToken = System.getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN");
            if (reqToken == null || reqToken.isEmpty()) {
                throw new GradleException("ACTIONS_ID_TOKEN_REQUEST_URL is set but ACTIONS_ID_TOKEN_REQUEST_TOKEN is not");
            }
            String audience = URLEncoder.encode(a.base.toString(), StandardCharsets.UTF_8);
            URI url = URI.create(reqUrl + (reqUrl.contains("?") ? "&" : "?") + "audience=" + audience);
            String token = Json.string(a.getBearer("GitHub Actions identity token request", url, reqToken), "value");
            if (token == null || token.isEmpty()) {
                throw new GradleException("GitHub Actions returned no identity token");
            }
            return token;
        }
        String env = getParameters().getOidcTokenEnv().getOrNull();
        if (env != null && !env.isEmpty()) {
            String token = System.getenv(env);
            if (token != null && !token.trim().isEmpty()) {
                return token.trim();
            }
        }
        return null;
    }

    /** Ends every session this build minted. A failure is a warning: the session expires anyway. */
    @Override
    public void close() {
        for (Map.Entry<String, Session> e : sessions.entrySet()) {
            Session s = e.getValue();
            try {
                api().delete("/api/publish/sessions/" + URLEncoder.encode(s.id, StandardCharsets.UTF_8), SESSION_USER, s.token);
            } catch (RuntimeException ex) {
                LOG.warn("Telesphorepo: could not close publish session {} for {}: {}", s.id, e.getKey(), ex.getMessage());
            }
        }
        sessions.clear();
    }
}
