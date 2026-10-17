# The Jenkins of the clients zone (TestCLIJenkins): the newest Jenkins LTS with the newest
# OpenID Connect Provider, Pipeline, Credentials Binding and Git plugins; Maven for the Maven job;
# the Docker CLI for the Docker job, which talks to a Docker in Docker engine (DOCKER_HOST).
# The test builds it with --pull, and E2E_DAY (the date) makes it fetch the plugins again each day.
FROM docker.io/library/docker:cli AS docker

FROM docker.io/jenkins/jenkins:lts
USER root
RUN apt-get update && apt-get install -y --no-install-recommends maven && rm -rf /var/lib/apt/lists/*
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
ARG E2E_DAY
RUN echo "plugins of ${E2E_DAY}" && jenkins-plugin-cli --plugins oidc-provider workflow-aggregator credentials-binding git
COPY init.groovy.d/ /usr/share/jenkins/ref/init.groovy.d/
# No setup wizard; a Pipeline job may check out the local git repositories of the test.
ENV JAVA_OPTS="-Djenkins.install.runSetupWizard=false -Dhudson.plugins.git.GitSCM.ALLOW_LOCAL_CHECKOUT=true"
