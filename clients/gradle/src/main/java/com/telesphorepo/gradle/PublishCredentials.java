package com.telesphorepo.gradle;

import org.gradle.api.Action;
import org.gradle.api.Task;
import org.gradle.api.artifacts.repositories.MavenArtifactRepository;
import org.gradle.api.artifacts.repositories.PasswordCredentials;
import org.gradle.api.provider.Provider;
import org.gradle.api.publish.maven.tasks.PublishToMavenRepository;

/**
 * Runs first in every {@link PublishToMavenRepository} task. When the task's repository is on the
 * configured server, it sets the repository's credentials to the build's publish session for
 * that repository, so a session is minted only when something is about to be published.
 */
final class PublishCredentials implements Action<Task> {
    private final Provider<TelesphorepoService> service;

    PublishCredentials(Provider<TelesphorepoService> service) {
        this.service = service;
    }

    @Override
    public void execute(Task task) {
        MavenArtifactRepository repo = ((PublishToMavenRepository) task).getRepository();
        if (repo == null) {
            return;
        }
        TelesphorepoService s = service.get();
        String name = s.repoName(repo.getUrl());
        if (name == null) {
            return;
        }
        String token = s.token(name);
        PasswordCredentials c = repo.getCredentials();
        c.setUsername(TelesphorepoService.SESSION_USER);
        c.setPassword(token);
    }
}
