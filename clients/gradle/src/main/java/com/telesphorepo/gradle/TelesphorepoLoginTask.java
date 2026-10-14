package com.telesphorepo.gradle;

import org.gradle.api.DefaultTask;
import org.gradle.api.GradleException;
import org.gradle.api.provider.Property;
import org.gradle.api.tasks.Input;
import org.gradle.api.tasks.Optional;
import org.gradle.api.tasks.TaskAction;
import org.gradle.api.tasks.UntrackedTask;

import java.net.InetAddress;
import java.security.KeyPair;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * Enrolls this machine as a device: it generates an Ed25519 key, asks the server for a code,
 * prints the approval URL, waits until the user approves it in a browser, then saves the key in
 * {@code ~/.telesphorepo/<host>/device.json}.
 */
@UntrackedTask(because = "Enrolls a device with the server")
public abstract class TelesphorepoLoginTask extends DefaultTask {
    @Input
    @Optional
    public abstract Property<String> getUrl();

    @TaskAction
    public void login() {
        if (!getUrl().isPresent()) {
            throw new GradleException("Set the server URL in settings.gradle(.kts): telesphorepo { url = \"https://repo.example.com\" }");
        }
        Api api = new Api(getUrl().get());
        KeyPair keys = DeviceKey.generate();

        Map<String, Object> enroll = new LinkedHashMap<>();
        enroll.put("name", deviceName());
        enroll.put("kind", "gradle");
        enroll.put("public_key", DeviceKey.publicKey(keys.getPublic()));
        Map<String, Object> resp = api.post("/api/devices/enroll", enroll);
        String deviceCode = Json.string(resp, "device_code");
        String userCode = Json.string(resp, "user_code");
        String verificationUrl = Json.string(resp, "verification_url");
        if (deviceCode == null || userCode == null || verificationUrl == null) {
            throw new GradleException("The server's enrollment answer is incomplete");
        }
        long interval = Math.max(1, Json.number(resp, "interval", 5));
        long deadline = System.currentTimeMillis() + 1000 * Json.number(resp, "expires_in", 600);

        getLogger().lifecycle("Open " + verificationUrl + "?code=" + userCode + " and approve code " + userCode);

        Map<String, Object> poll = new LinkedHashMap<>();
        poll.put("device_code", deviceCode);
        long deviceId = 0;
        while (deviceId <= 0) {
            if (System.currentTimeMillis() >= deadline) {
                throw new GradleException("The code expired before anyone approved it. Run ./gradlew telesphorepoLogin again.");
            }
            sleep(interval);
            try {
                deviceId = Json.number(api.post("/api/devices/enroll/poll", poll), "device_id", 0);
                if (deviceId <= 0) {
                    throw new GradleException("The server's poll answer has no device_id");
                }
            } catch (Api.ApiException e) {
                switch (e.error) {
                    case "authorization_pending":
                        break;
                    case "slow_down":
                        interval += 5;
                        break;
                    case "expired_token":
                        throw new GradleException("The code expired before anyone approved it. Run ./gradlew telesphorepoLogin again.");
                    default:
                        throw e;
                }
            }
        }

        DeviceKey.save(api.base, deviceId, keys.getPrivate());
        getLogger().lifecycle("Device " + deviceId + " is enrolled. Its key is in " + DeviceKey.file(api.base));
    }

    private static void sleep(long seconds) {
        try {
            Thread.sleep(seconds * 1000);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new GradleException("telesphorepoLogin was interrupted", e);
        }
    }

    /** "user@host", the device name that the approval page and the device list show. */
    private static String deviceName() {
        String host = System.getenv("COMPUTERNAME");
        if (host == null || host.isEmpty()) {
            host = System.getenv("HOSTNAME");
        }
        if (host == null || host.isEmpty()) {
            try {
                host = InetAddress.getLocalHost().getHostName();
            } catch (Exception e) {
                host = "unknown";
            }
        }
        return System.getProperty("user.name") + "@" + host;
    }
}
