package com.telesphorepo.maven.client;

import java.io.IOException;
import java.io.InterruptedIOException;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Map;

/** JSON calls to a Telesphorepo server. Request and answer bodies are never logged. */
public final class Api {
    /** The HTTP Basic username sent with a publish-session token. The server ignores it. */
    public static final String USERNAME = "telesphorepo";

    private static final Duration TIMEOUT = Duration.ofSeconds(60);

    private final ServerUrl server;
    private final HttpClient http;

    public Api(ServerUrl server) {
        this.server = server;
        this.http = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(30))
                .followRedirects(HttpClient.Redirect.NEVER)
                .build();
    }

    public ServerUrl server() {
        return server;
    }

    /**
     * POSTs {@code body} as JSON to {@code path} on the server and returns the answer object.
     * {@code token}, when not null, is sent as the HTTP Basic password.
     */
    public Map<String, Object> post(String path, Map<String, ?> body, String token) throws IOException {
        HttpRequest.Builder b = request(server.base() + path, token)
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(Json.write(body), StandardCharsets.UTF_8));
        return send(b.build(), "POST " + path);
    }

    /** Sends DELETE to {@code path} with {@code token} as the HTTP Basic password. */
    public void delete(String path, String token) throws IOException {
        send(request(server.base() + path, token).DELETE().build(), "DELETE " + path);
    }

    /**
     * Gets the GitHub Actions identity token for {@code audience} from the runner's token
     * endpoint ({@code ACTIONS_ID_TOKEN_REQUEST_URL} and {@code ACTIONS_ID_TOKEN_REQUEST_TOKEN}).
     */
    public String githubIdToken(String requestUrl, String requestToken, String audience) throws IOException {
        String url = requestUrl + (requestUrl.contains("?") ? "&" : "?")
                + "audience=" + URLEncoder.encode(audience, StandardCharsets.UTF_8);
        HttpRequest req = HttpRequest.newBuilder(URI.create(url))
                .timeout(TIMEOUT)
                .header("Accept", "application/json")
                .header("Authorization", "bearer " + requestToken)
                .GET()
                .build();
        Map<String, Object> answer = send(req, "GitHub Actions ID token request");
        Object value = answer.get("value");
        if (!(value instanceof String s) || s.isEmpty()) {
            throw new IOException("GitHub Actions ID token request: the answer has no token");
        }
        return s;
    }

    private HttpRequest.Builder request(String url, String token) {
        HttpRequest.Builder b = HttpRequest.newBuilder(URI.create(url))
                .timeout(TIMEOUT)
                .header("Accept", "application/json");
        if (token != null) {
            String basic = Base64.getEncoder()
                    .encodeToString((USERNAME + ":" + token).getBytes(StandardCharsets.UTF_8));
            b.header("Authorization", "Basic " + basic);
        }
        return b;
    }

    private Map<String, Object> send(HttpRequest req, String what) throws IOException {
        HttpResponse<String> resp;
        try {
            resp = http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new InterruptedIOException(what + ": interrupted");
        } catch (IOException e) {
            throw new IOException(what + ": " + e.getMessage(), e);
        }
        Map<String, Object> body = Json.parseObject(resp.body());
        if (resp.statusCode() / 100 != 2) {
            Object err = body.get("error");
            throw new ApiException(what, resp.statusCode(), err instanceof String s ? s : "");
        }
        return body;
    }
}
