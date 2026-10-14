package com.telesphorepo.gradle;

import org.gradle.api.GradleException;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Map;

/** Calls the Telesphorepo JSON API. Error messages never contain a token, key or code. */
final class Api {
    private static final String USER_AGENT = "telesphorepo-gradle-plugin";

    /** The server's base URL without a trailing slash, e.g. {@code https://repo.acme.com}. */
    final URI base;
    private final HttpClient http = HttpClient.newBuilder()
        .version(HttpClient.Version.HTTP_1_1)
        .connectTimeout(Duration.ofSeconds(30))
        .followRedirects(HttpClient.Redirect.NEVER)
        .build();

    Api(String url) {
        this.base = normalize(url);
    }

    /** Parses url, checks that it is http(s) with a host, and drops trailing slashes. */
    static URI normalize(String url) {
        String u = url == null ? "" : url.trim();
        while (u.endsWith("/")) {
            u = u.substring(0, u.length() - 1);
        }
        URI uri;
        try {
            uri = URI.create(u);
        } catch (IllegalArgumentException e) {
            throw new GradleException("telesphorepo.url is not a valid URL: " + url);
        }
        String scheme = uri.getScheme();
        if (uri.getHost() == null || !("http".equalsIgnoreCase(scheme) || "https".equalsIgnoreCase(scheme))) {
            throw new GradleException("telesphorepo.url must be an http(s) URL with a host, e.g. https://repo.example.com (got " + url + ")");
        }
        return uri;
    }

    /** An HTTP error answer: the status and the server's {@code error} field. */
    static final class ApiException extends GradleException {
        final int status;
        final String error;

        ApiException(String what, int status, String error) {
            super(what + " failed: HTTP " + status + (error.isEmpty() ? "" : ": " + error));
            this.status = status;
            this.error = error;
        }
    }

    /** POSTs body as JSON to base + path and returns the JSON object answer. */
    Map<String, Object> post(String path, Map<String, ?> body) {
        HttpRequest req = request(path)
            .header("Content-Type", "application/json")
            .POST(HttpRequest.BodyPublishers.ofString(Json.write(body)))
            .build();
        return Json.parseObject(send("POST " + path, req));
    }

    /** DELETEs base + path with HTTP Basic credentials. */
    void delete(String path, String username, String password) {
        HttpRequest req = request(path)
            .header("Authorization", basic(username, password))
            .DELETE()
            .build();
        send("DELETE " + path, req);
    }

    /** GETs an absolute URL with a bearer token and returns the JSON object answer. */
    Map<String, Object> getBearer(String what, URI url, String bearer) {
        HttpRequest req = HttpRequest.newBuilder(url)
            .timeout(Duration.ofSeconds(60))
            .header("User-Agent", USER_AGENT)
            .header("Accept", "application/json")
            .header("Authorization", "bearer " + bearer)
            .GET()
            .build();
        return Json.parseObject(send(what, req));
    }

    private HttpRequest.Builder request(String path) {
        return HttpRequest.newBuilder(URI.create(base + path))
            .timeout(Duration.ofSeconds(60))
            .header("User-Agent", USER_AGENT)
            .header("Accept", "application/json");
    }

    private String send(String what, HttpRequest req) {
        HttpResponse<String> resp;
        try {
            resp = http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
        } catch (IOException e) {
            throw new GradleException(what + " failed: " + e.getMessage(), e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new GradleException(what + " was interrupted", e);
        }
        int status = resp.statusCode();
        String body = resp.body() == null ? "" : resp.body();
        if (status >= 200 && status < 300) {
            return body.isEmpty() ? "{}" : body;
        }
        throw new ApiException(what, status, errorField(body));
    }

    private static String errorField(String body) {
        try {
            String e = Json.string(Json.parseObject(body), "error");
            return e == null ? "" : e;
        } catch (IllegalArgumentException e) {
            return "";
        }
    }

    private static String basic(String username, String password) {
        String raw = username + ":" + password;
        return "Basic " + Base64.getEncoder().encodeToString(raw.getBytes(StandardCharsets.UTF_8));
    }
}
