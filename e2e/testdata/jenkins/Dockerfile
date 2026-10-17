# The Jenkins of the clients zone (TestCLIJenkins): a Jenkins LTS with the OpenID Connect Provider
# plugin, Pipeline, Credentials Binding and Git; Maven for the Maven job; the Docker CLI for the
# Docker job, which talks to a Docker in Docker engine (DOCKER_HOST).
FROM docker:29.9.0-dind AS docker

FROM jenkins/jenkins:2.580.1-lts-jdk21
USER root
RUN apt-get update && apt-get install -y --no-install-recommends maven && rm -rf /var/lib/apt/lists/*
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
RUN jenkins-plugin-cli --plugins \
    oidc-provider:234.vca_b_26b_f4b_dda_ \
    workflow-aggregator:608.v67378e9d3db_1 \
    credentials-binding:728.v902a_273b_8947 \
    git:5.10.1
COPY init.groovy.d/ /usr/share/jenkins/ref/init.groovy.d/
# No setup wizard; a Pipeline job may check out the local git repositories of the test.
ENV JAVA_OPTS="-Djenkins.install.runSetupWizard=false -Dhudson.plugins.git.GitSCM.ALLOW_LOCAL_CHECKOUT=true"
