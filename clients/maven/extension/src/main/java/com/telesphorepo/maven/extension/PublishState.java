package com.telesphorepo.maven.extension;

import com.telesphorepo.maven.client.Publisher;
import com.telesphorepo.maven.client.ServerUrl;
import java.io.IOException;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import javax.inject.Named;
import javax.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * The state of one Maven build: the configured server, the sessions minted so far, and whether
 * the current thread runs a deploy goal. The lifecycle participant, the event spy and the
 * authentication selector share it.
 */
@Named
@Singleton
public class PublishState {
    static final String LOGIN = "mvn telesphorepo:login";

    private static final Logger log = LoggerFactory.getLogger(PublishState.class);

    private final ThreadLocal<Boolean> deploying = ThreadLocal.withInitial(() -> Boolean.FALSE);
    private final Set<String> reported = ConcurrentHashMap.newKeySet();
    private final Set<String> announced = ConcurrentHashMap.newKeySet();
    private volatile Publisher publisher;

    void configure(ServerUrl server, String oidcTokenEnv) {
        String login = LOGIN + " -Dtelesphorepo.url=" + server.base();
        publisher = new Publisher(server, oidcTokenEnv, System.getenv(), login);
    }

    /** The configured server, or null when the build sets no telesphorepo.url. */
    ServerUrl server() {
        Publisher p = publisher;
        return p == null ? null : p.server();
    }

    /** Returns null when a proof of identity is available, else what the developer should do. */
    String missingProof() {
        Publisher p = publisher;
        return p == null ? null : p.missingProof();
    }

    void deployStarted() {
        deploying.set(Boolean.TRUE);
    }

    void deployEnded() {
        deploying.remove();
    }

    boolean deploying() {
        return deploying.get();
    }

    /**
     * Returns a publish-session token for {@code repoUrl} when it is a repository on the
     * configured server, minting a session when needed. Returns null for other URLs and when
     * minting fails; the failure is logged once per repository.
     */
    String token(String repoUrl) {
        Publisher p = publisher;
        if (p == null) {
            return null;
        }
        String repo = p.server().repoOf(repoUrl);
        if (repo == null) {
            return null;
        }
        try {
            Publisher.Session s = p.session(repo);
            reported.remove(repo);
            if (announced.add(s.id)) {
                log.info("Telesphorepo: publish session {} for {} ({})", s.id, s.repo, s.via);
            }
            return s.token;
        } catch (IOException e) {
            if (reported.add(repo)) {
                log.error("Telesphorepo: cannot start a publish session for repository {}: {}", repo, e.getMessage());
            }
            return null;
        }
    }

    /** Closes every session of this build. */
    void closeAll() {
        Publisher p = publisher;
        if (p != null) {
            p.closeAll(msg -> log.warn("Telesphorepo: {}", msg));
        }
    }
}
