package com.telesphorepo.maven.client;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Locale;

/**
 * The base URL of a Telesphorepo server, for example {@code https://repo.acme.com}. It names the
 * device key directory and tells which repository URLs belong to the server.
 */
public final class ServerUrl {
    private final String scheme;
    private final String host;
    private final int port;
    private final String path;
    private final int namedPort;

    private ServerUrl(String scheme, String host, int port, String path, int namedPort) {
        this.scheme = scheme;
        this.host = host;
        this.port = port;
        this.path = path;
        this.namedPort = namedPort;
    }

    /** Parses an http or https URL. A trailing slash is ignored. */
    public static ServerUrl parse(String url) {
        URI u = uri(url == null ? "" : url.trim());
        if (u == null || u.getHost() == null || !("http".equals(scheme(u)) || "https".equals(scheme(u)))) {
            throw new IllegalArgumentException("telesphorepo.url must be an http or https URL, got \"" + url + "\"");
        }
        String p = u.getRawPath() == null ? "" : u.getRawPath();
        while (p.endsWith("/")) {
            p = p.substring(0, p.length() - 1);
        }
        return new ServerUrl(scheme(u), u.getHost().toLowerCase(Locale.ROOT), port(u), p, u.getPort());
    }

    /** The URL without a trailing slash. API paths are appended to it. */
    public String base() {
        return scheme + "://" + host + (port < 0 ? "" : ":" + port) + path;
    }

    /** The host, plus "_" and the port when the URL names one, even 443: the directory under ~/.telesphorepo. */
    public String hostDir() {
        String h = host.replace("[", "").replace("]", "");
        String dir = namedPort < 0 ? h : h + "_" + namedPort;
        return dir.replaceAll("[^a-z0-9._-]", "_");
    }

    /**
     * Returns the Telesphorepo repository name when {@code url} is a Maven repository on this
     * server ({@code <base>/maven/<repo>/...}), else null.
     */
    public String repoOf(String url) {
        if (url == null) {
            return null;
        }
        URI u = uri(url.trim());
        if (u == null || u.getHost() == null || !scheme.equals(scheme(u))
                || !host.equals(u.getHost().toLowerCase(Locale.ROOT)) || port != port(u)) {
            return null;
        }
        String p = u.getRawPath() == null ? "" : u.getRawPath();
        String prefix = path + "/maven/";
        if (!p.startsWith(prefix)) {
            return null;
        }
        String rest = p.substring(prefix.length());
        int slash = rest.indexOf('/');
        String repo = slash < 0 ? rest : rest.substring(0, slash);
        return repo.isEmpty() ? null : repo;
    }

    @Override
    public boolean equals(Object o) {
        return o instanceof ServerUrl other && base().equals(other.base());
    }

    @Override
    public int hashCode() {
        return base().hashCode();
    }

    @Override
    public String toString() {
        return base();
    }

    private static URI uri(String url) {
        try {
            return new URI(url);
        } catch (URISyntaxException e) {
            return null;
        }
    }

    private static String scheme(URI u) {
        return u.getScheme() == null ? "" : u.getScheme().toLowerCase(Locale.ROOT);
    }

    private static int port(URI u) {
        int p = u.getPort();
        String s = scheme(u);
        if (("http".equals(s) && p == 80) || ("https".equals(s) && p == 443)) {
            return -1;
        }
        return p;
    }
}
