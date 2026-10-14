package com.telesphorepo.maven.plugin;

import com.telesphorepo.maven.client.DeviceLogin;
import com.telesphorepo.maven.client.ServerUrl;
import java.io.IOException;
import java.net.InetAddress;
import org.apache.maven.plugin.AbstractMojo;
import org.apache.maven.plugin.MojoExecutionException;
import org.apache.maven.plugins.annotations.Mojo;
import org.apache.maven.plugins.annotations.Parameter;

/**
 * Enrolls this machine as a Telesphorepo publishing device. Run
 * {@code mvn telesphorepo:login -Dtelesphorepo.url=https://repo.acme.com}, open the URL it
 * prints, sign in and approve the code. The key goes to
 * {@code ~/.telesphorepo/<host>/device.json}; the Telesphorepo extension then mints publish
 * sessions with it.
 */
@Mojo(name = "login", requiresProject = false, threadSafe = true)
public class LoginMojo extends AbstractMojo {
    /** The Telesphorepo server. Falls back to the environment variable TELESPHOREPO_URL. */
    @Parameter(property = "telesphorepo.url")
    private String url;

    /** The device name the approval page and the device list show. Defaults to "&lt;user&gt;@&lt;hostname&gt;". */
    @Parameter(property = "telesphorepo.deviceName")
    private String deviceName;

    @Override
    public void execute() throws MojoExecutionException {
        String u = url != null && !url.isBlank() ? url : System.getenv("TELESPHOREPO_URL");
        if (u == null || u.isBlank()) {
            throw new MojoExecutionException(
                    "Set the server: mvn telesphorepo:login -Dtelesphorepo.url=https://repo.example.com");
        }
        ServerUrl server;
        try {
            server = ServerUrl.parse(u);
        } catch (IllegalArgumentException e) {
            throw new MojoExecutionException(e.getMessage(), e);
        }
        String name = deviceName != null && !deviceName.isBlank() ? deviceName.trim() : System.getProperty("user.name") + "@" + hostname();
        try {
            DeviceLogin.run(server, name, "maven", line -> getLog().info(line));
        } catch (IOException e) {
            throw new MojoExecutionException("Telesphorepo login failed: " + e.getMessage(), e);
        }
    }

    private static String hostname() {
        for (String var : new String[] {"COMPUTERNAME", "HOSTNAME"}) {
            String v = System.getenv(var);
            if (v != null && !v.isBlank()) {
                return v.trim();
            }
        }
        try {
            return InetAddress.getLocalHost().getHostName();
        } catch (IOException e) {
            return "unknown host";
        }
    }
}
