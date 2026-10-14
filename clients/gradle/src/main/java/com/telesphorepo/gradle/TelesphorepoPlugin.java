package com.telesphorepo.gradle;

import org.gradle.api.Plugin;
import org.gradle.api.artifacts.repositories.MavenArtifactRepository;
import org.gradle.api.initialization.Settings;
import org.gradle.api.invocation.Gradle;
import org.gradle.api.provider.Provider;
import org.gradle.api.publish.maven.tasks.PublishToMavenRepository;

/**
 * Settings plugin {@code com.telesphorepo}. Every Maven repository under
 * {@code <telesphorepo.url>/maven/} gets a publish session as its credentials when a task first
 * publishes to it; the sessions end with the build. The root project gets
 * {@code telesphorepoLogin}, which enrolls this machine as a device.
 */
public class TelesphorepoPlugin implements Plugin<Settings> {
    @Override
    public void apply(Settings settings) {
        TelesphorepoExtension ext = settings.getExtensions().create("telesphorepo", TelesphorepoExtension.class);
        Gradle gradle = settings.getGradle();

        Provider<TelesphorepoService> service = gradle.getSharedServices().registerIfAbsent(
            "telesphorepo", TelesphorepoService.class, spec -> {
                spec.getParameters().getUrl().set(ext.getUrl());
                spec.getParameters().getOidcTokenEnv().set(ext.getOidcTokenEnv());
            });

        gradle.rootProject(project -> project.getTasks().register("telesphorepoLogin", TelesphorepoLoginTask.class, task -> {
            task.setGroup("publishing");
            task.setDescription("Enrolls this machine as a device that can publish to the Telesphorepo server.");
            task.getUrl().set(ext.getUrl());
        }));

        gradle.allprojects(project -> project.getTasks().withType(PublishToMavenRepository.class).configureEach(task -> {
            task.usesService(service);
            task.doFirst(new PublishCredentials(service));
            // maven-publish sets the repository in the register action, which runs before this one.
            // A configuration cache hit rebuilds the repository with credentials from Gradle
            // properties only, so a task that publishes to the server cannot use the cache.
            if (mayPublishToServer(ext, task.getRepository())) {
                task.notCompatibleWithConfigurationCache("Telesphorepo sets the repository credentials when the task runs");
            }
        }));
    }

    /** True when repo is on the configured server, or when its URL is not known yet. */
    private static boolean mayPublishToServer(TelesphorepoExtension ext, MavenArtifactRepository repo) {
        String url = ext.getUrl().getOrNull();
        if (url == null) {
            return false;
        }
        if (repo == null || repo.getUrl() == null) {
            return true;
        }
        return TelesphorepoService.repoName(Api.normalize(url), repo.getUrl()) != null;
    }
}
