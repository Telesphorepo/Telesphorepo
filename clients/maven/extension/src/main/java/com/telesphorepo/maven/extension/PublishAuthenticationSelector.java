package com.telesphorepo.maven.extension;

import com.telesphorepo.maven.client.Api;
import org.eclipse.aether.repository.Authentication;
import org.eclipse.aether.repository.AuthenticationSelector;
import org.eclipse.aether.repository.RemoteRepository;
import org.eclipse.aether.util.repository.AuthenticationBuilder;

/**
 * While a deploy goal runs, gives each repository on the Telesphorepo server the publish-session
 * token as its password. This covers deployment repositories that are not in the project's
 * distributionManagement, such as -DaltDeploymentRepository and deploy:deploy-file. Every other
 * lookup goes to the selector Maven built from settings.xml.
 */
final class PublishAuthenticationSelector implements AuthenticationSelector {
    private final AuthenticationSelector settings;
    private final PublishState state;

    PublishAuthenticationSelector(AuthenticationSelector settings, PublishState state) {
        this.settings = settings;
        this.state = state;
    }

    @Override
    public Authentication getAuthentication(RemoteRepository repository) {
        if (state.deploying()) {
            String token = state.token(repository.getUrl());
            if (token != null) {
                return new AuthenticationBuilder().addUsername(Api.USERNAME).addPassword(token).build();
            }
        }
        return settings == null ? null : settings.getAuthentication(repository);
    }
}
