package com.telesphorepo.maven.extension;

import com.telesphorepo.maven.client.Api;
import javax.inject.Inject;
import javax.inject.Named;
import javax.inject.Singleton;
import org.apache.maven.artifact.repository.ArtifactRepository;
import org.apache.maven.artifact.repository.Authentication;
import org.apache.maven.eventspy.AbstractEventSpy;
import org.apache.maven.execution.ExecutionEvent;
import org.apache.maven.plugin.MojoExecution;
import org.apache.maven.project.MavenProject;

/**
 * Mints the publish session just before maven-deploy-plugin runs, so the session's time starts
 * at the upload and not at the start of the build. The token replaces the credentials of the
 * project's deployment repository; {@link PublishAuthenticationSelector} serves the other
 * repositories the goal deploys to.
 */
@Named("telesphorepo")
@Singleton
public class TelesphorepoEventSpy extends AbstractEventSpy {
    private final PublishState state;

    @Inject
    public TelesphorepoEventSpy(PublishState state) {
        this.state = state;
    }

    @Override
    public void onEvent(Object event) {
        if (!(event instanceof ExecutionEvent e)) {
            return;
        }
        MojoExecution mojo = e.getMojoExecution();
        if (mojo == null || !"maven-deploy-plugin".equals(mojo.getArtifactId())) {
            return;
        }
        switch (e.getType()) {
            case MojoStarted -> {
                state.deployStarted();
                MavenProject project = e.getProject();
                ArtifactRepository repo = project == null ? null : project.getDistributionManagementArtifactRepository();
                if (repo != null) {
                    String token = state.token(repo.getUrl());
                    if (token != null) {
                        repo.setAuthentication(new Authentication(Api.USERNAME, token));
                    }
                }
            }
            case MojoSucceeded, MojoFailed -> state.deployEnded();
            default -> {
            }
        }
    }
}
