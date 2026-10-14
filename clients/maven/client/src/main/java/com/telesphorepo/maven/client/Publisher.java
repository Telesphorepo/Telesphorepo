package com.telesphorepo.maven.client;

import java.io.IOException;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.OffsetDateTime;
import java.time.format.DateTimeParseException;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;

/**
 * Mints one publish session per Telesphorepo repository and closes them all at the end of the
 * build. The proof of identity is, in this order: the GitHub Actions identity token (when
 * {@code ACTIONS_ID_TOKEN_REQUEST_URL} and {@code ACTIONS_ID_TOKEN_REQUEST_TOKEN} are set), the
 * OIDC token in the environment variable named by {@code oidcTokenEnv}, and the enrolled device
 * key in {@code ~/.telesphorepo/<host>/device.json}.
 */
public final class Publisher {
    /** A session is minted again when the cached one has less than this left. */
    private static final long MIN_REMAINING_SECONDS = 60;

    private final ServerUrl server;
    private final String oidcTokenEnv;
    private final Map<String, String> env;
    private final String loginCommand;
    private final Api api;
    private final Map<String, Session> current = new HashMap<>();
    private final List<Session> minted = new ArrayList<>();

    /**
     * @param oidcTokenEnv the name of the variable that holds a CI's OIDC token, or null
     * @param loginCommand the command that enrolls a device, shown when there is no proof
     */
    public Publisher(ServerUrl server, String oidcTokenEnv, Map<String, String> env, String loginCommand) {
        this.server = server;
        this.oidcTokenEnv = oidcTokenEnv == null || oidcTokenEnv.isBlank() ? null : oidcTokenEnv.trim();
        this.env = env;
        this.loginCommand = loginCommand;
        this.api = new Api(server);
    }

    public ServerUrl server() {
        return server;
    }

    /** An open publish session. {@code token} is the HTTP Basic password for the publish. */
    public static final class Session {
        public final String id;
        public final String token;
        public final String repo;
        public final long expiresAt;
        /** The proof that minted the session, for example "device 7". */
        public final String via;

        Session(String id, String token, String repo, long expiresAt, String via) {
            this.id = id;
            this.token = token;
            this.repo = repo;
            this.expiresAt = expiresAt;
            this.via = via;
        }

        @Override
        public String toString() {
            return "publish session " + id + " for " + repo + " (" + via + ")";
        }
    }

    /** Returns null when a proof is available, else a message that tells how to get one. */
    public String missingProof() {
        if (githubAvailable() || envToken() != null) {
            return null;
        }
        try {
            if (DeviceKey.load(server) != null) {
                return null;
            }
        } catch (IOException e) {
            return e.getMessage() + ". Run " + loginCommand + " to enroll this machine again.";
        }
        return "This machine has no Telesphorepo device key for " + server + ". Run " + loginCommand
                + " to enroll it, or publish from a CI with an OIDC trust policy.";
    }

    /** Returns an open session for {@code repo}, minting one when there is none or it ends soon. */
    public synchronized Session session(String repo) throws IOException {
        Session s = current.get(repo);
        long now = Instant.now().getEpochSecond();
        if (s != null && s.expiresAt - now > MIN_REMAINING_SECONDS) {
            return s;
        }
        s = mint(repo);
        current.put(repo, s);
        minted.add(s);
        return s;
    }

    /** Closes every session this publisher minted. {@code warn} receives failures. */
    public synchronized void closeAll(Consumer<String> warn) {
        for (Session s : minted) {
            try {
                api.delete("/api/publish/sessions/" + URLEncoder.encode(s.id, StandardCharsets.UTF_8), s.token);
            } catch (ApiException e) {
                // The session already ended (expired or revoked): nothing is left to close.
                if (e.status() != 401 && e.status() != 404) {
                    warn.accept("cannot close " + s + ": " + e.getMessage());
                }
            } catch (IOException e) {
                warn.accept("cannot close " + s + ": " + e.getMessage());
            }
        }
        minted.clear();
        current.clear();
    }

    private Session mint(String repo) throws IOException {
        if (githubAvailable()) {
            String token = api.githubIdToken(env.get("ACTIONS_ID_TOKEN_REQUEST_URL"),
                    env.get("ACTIONS_ID_TOKEN_REQUEST_TOKEN"), server.base());
            return oidc(token, repo, "GitHub Actions");
        }
        String envToken = envToken();
        if (envToken != null) {
            return oidc(envToken, repo, "the CI identity token in " + oidcTokenEnv);
        }
        DeviceKey device = DeviceKey.load(server);
        if (device == null) {
            throw new IOException(missingProof());
        }
        Map<String, Object> challenge;
        try {
            challenge = api.post("/api/publish/challenge", Map.of("device_id", device.deviceId()), null);
        } catch (ApiException e) {
            if (e.status() == 401 || e.status() == 404) {
                throw refused(device, e);
            }
            throw e;
        }
        if (!(challenge.get("nonce") instanceof String nonce) || nonce.isEmpty()) {
            throw new IOException("the challenge answer has no nonce");
        }
        Map<String, Object> req = new LinkedHashMap<>();
        req.put("device_id", device.deviceId());
        req.put("repo", repo);
        req.put("nonce", nonce);
        req.put("signature", device.signPublish(nonce, repo));
        try {
            return session(api.post("/api/publish/sessions", req, null), repo, "device " + device.deviceId());
        } catch (ApiException e) {
            if (e.status() == 401) {
                throw refused(device, e);
            }
            throw e;
        }
    }

    private IOException refused(DeviceKey device, ApiException e) {
        return new IOException("the server refused device " + device.deviceId() + " (" + e.getMessage()
                + "). It may be revoked or expired; run " + loginCommand + " to enroll this machine again.", e);
    }

    /**
     * The token in the variable named by oidcTokenEnv, or null when the variable is unset or
     * empty. An empty variable falls back to the device key, so a project can name the variable
     * for its CI and still publish from a developer machine.
     */
    private String envToken() {
        if (oidcTokenEnv == null) {
            return null;
        }
        String token = env.get(oidcTokenEnv);
        return token == null || token.isBlank() ? null : token.trim();
    }

    private Session oidc(String token, String repo, String via) throws IOException {
        Map<String, Object> req = new LinkedHashMap<>();
        req.put("token", token);
        req.put("repo", repo);
        return session(api.post("/api/publish/sessions/oidc", req, null), repo, via);
    }

    private static Session session(Map<String, Object> m, String repo, String via) throws IOException {
        Object id = m.get("id");
        Object token = m.get("token");
        if (id == null || !(token instanceof String t) || t.isEmpty()) {
            throw new IOException("the publish session answer has no id or token");
        }
        return new Session(String.valueOf(id), t, repo, epochSeconds(m.get("expires_at")), via);
    }

    /** Reads expires_at as unix seconds, unix milliseconds or an RFC 3339 time. */
    private static long epochSeconds(Object v) {
        if (v instanceof Long n) {
            return n > 100_000_000_000L ? n / 1000 : n;
        }
        if (v instanceof String s) {
            try {
                return OffsetDateTime.parse(s).toEpochSecond();
            } catch (DateTimeParseException e) {
                // Fall through to the minimum lifetime below.
            }
        }
        // An unreadable expiry: reuse the session for at most two minutes.
        return Instant.now().getEpochSecond() + MIN_REMAINING_SECONDS * 2;
    }

    private boolean githubAvailable() {
        String url = env.get("ACTIONS_ID_TOKEN_REQUEST_URL");
        String token = env.get("ACTIONS_ID_TOKEN_REQUEST_TOKEN");
        return url != null && !url.isBlank() && token != null && !token.isBlank();
    }
}
