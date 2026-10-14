package com.telesphorepo.maven.extension;

import com.telesphorepo.maven.client.ServerUrl;
import java.util.List;
import javax.inject.Inject;
import javax.inject.Named;
import javax.inject.Singleton;
import org.apache.maven.AbstractMavenLifecycleParticipant;
import org.apache.maven.MavenExecutionException;
import org.apache.maven.artifact.repository.ArtifactRepository;
import org.apache.maven.execution.MavenSession;
import org.apache.maven.project.MavenProject;
import org.eclipse.aether.DefaultRepositorySystemSession;
import org.eclipse.aether.RepositorySystemSession;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Sets up publishing for a build. The server comes from the property {@code telesphorepo.url}
 * (-D, then the top-level project), else the environment variable {@code TELESPHOREPO_URL}.
 * Without it the extension does nothing. A deploy to the server with no device key and no CI
 * token fails before the build starts. All sessions close when the build ends.
 */
@Named("telesphorepo")
@Singleton
public class TelesphorepoLifecycleParticipant extends AbstractMavenLifecycleParticipant {
    private static final Logger log = LoggerFactory.getLogger(TelesphorepoLifecycleParticipant.class);

    private final PublishState state;

    @Inject
    public TelesphorepoLifecycleParticipant(PublishState state) {
        this.state = state;
    }

    /** Wraps the authentication selector; Maven makes the session read-only after this call. */
    @Override
    public void afterSessionStart(MavenSession session) {
        RepositorySystemSession repo = session.getRepositorySession();
        if (repo instanceof DefaultRepositorySystemSession d) {
            d.setAuthenticationSelector(new PublishAuthenticationSelector(d.getAuthenticationSelector(), state));
        } else {
            log.warn("Telesphorepo: unknown repository session type {}; only distributionManagement repositories get publish sessions",
                    repo == null ? "null" : repo.getClass().getName());
        }
    }

    @Override
    public void afterProjectsRead(MavenSession session) throws MavenExecutionException {
        MavenProject top = session.getTopLevelProject();
        String url = property(session, top, "telesphorepo.url");
        if (url == null) {
            url = System.getenv("TELESPHOREPO_URL");
        }
        if (url == null || url.isBlank()) {
            log.debug("Telesphorepo: no telesphorepo.url; publish sessions are off");
            return;
        }
        ServerUrl server;
        try {
            server = ServerUrl.parse(url);
        } catch (IllegalArgumentException e) {
            throw new MavenExecutionException(e.getMessage(), top == null ? null : top.getFile());
        }
        state.configure(server, property(session, top, "telesphorepo.oidcTokenEnv"));

        if (!deploys(session.getGoals()) || !targetsServer(session.getProjects(), server)) {
            return;
        }
        String missing = state.missingProof();
        if (missing != null) {
            throw new MavenExecutionException(missing, top == null ? null : top.getFile());
        }
    }

    @Override
    public void afterSessionEnd(MavenSession session) {
        state.closeAll();
    }

    private static String property(MavenSession session, MavenProject top, String key) {
        String v = session.getUserProperties().getProperty(key);
        if (v == null) {
            v = session.getSystemProperties().getProperty(key);
        }
        if (v == null && top != null) {
            v = top.getProperties().getProperty(key);
        }
        return v == null || v.isBlank() ? null : v.trim();
    }

    private static boolean deploys(List<String> goals) {
        for (String g : goals) {
            if (g.equals("deploy") || g.endsWith(":deploy") || g.endsWith(":deploy-file")) {
                return true;
            }
        }
        return false;
    }

    private static boolean targetsServer(List<MavenProject> projects, ServerUrl server) {
        for (MavenProject p : projects) {
            // The snapshot repository for a -SNAPSHOT version, else the release repository.
            ArtifactRepository r = p.getDistributionManagementArtifactRepository();
            if (r != null && server.repoOf(r.getUrl()) != null) {
                return true;
            }
        }
        return false;
    }
}
