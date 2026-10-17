// Runs at each start of the Jenkins of TestCLIJenkins. The Jenkins URL is E2E_JENKINS_URL, so the
// issuer of the OpenID Connect Provider plugin is E2E_JENKINS_URL + "oidc" and each job URL starts
// with it. The credential telesphorepo-id-token is the "OpenID Connect id token" that the
// Jenkinsfiles of the server bind to OIDC_TOKEN, with the audience E2E_AUDIENCE (the server).
import com.cloudbees.plugins.credentials.CredentialsScope
import com.cloudbees.plugins.credentials.SystemCredentialsProvider
import com.cloudbees.plugins.credentials.domains.Domain
import hudson.security.AuthorizationStrategy
import hudson.security.SecurityRealm
import io.jenkins.plugins.oidc_provider.IdTokenStringCredentials
import jenkins.model.Jenkins
import jenkins.model.JenkinsLocationConfiguration

def j = Jenkins.get()
j.setSecurityRealm(SecurityRealm.NO_AUTHENTICATION)
j.setAuthorizationStrategy(AuthorizationStrategy.UNSECURED)
j.save()

def loc = JenkinsLocationConfiguration.get()
loc.setUrl(System.getenv('E2E_JENKINS_URL'))
loc.save()

def store = SystemCredentialsProvider.getInstance().getStore()
if (store.getCredentials(Domain.global()).every { it.id != 'telesphorepo-id-token' }) {
    def c = new IdTokenStringCredentials(CredentialsScope.GLOBAL, 'telesphorepo-id-token', 'Telesphorepo')
    c.setAudience(System.getenv('E2E_AUDIENCE'))
    store.addCredentials(Domain.global(), c)
}
