package com.telesphorepo.maven.client;

import java.io.IOException;
import java.io.InterruptedIOException;
import java.nio.file.Path;
import java.security.KeyPair;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.function.Consumer;

/**
 * Enrolls this machine as a publishing device with the device flow: the server gives a user
 * code, the developer approves it in a browser, and the client polls until the server answers
 * with a device id. The approved key is saved with {@link DeviceKey#save}.
 */
public final class DeviceLogin {
    private DeviceLogin() {
    }

    /**
     * Runs the enrollment and returns the path of the saved device file.
     *
     * @param kind "maven" or "gradle"
     * @param out  receives the lines to show the developer
     */
    public static Path run(ServerUrl server, String name, String kind, Consumer<String> out)
            throws IOException {
        Api api = new Api(server);
        KeyPair pair = DeviceKey.generate();
        Map<String, Object> req = new LinkedHashMap<>();
        req.put("name", name);
        req.put("kind", kind);
        req.put("public_key", Base64.getUrlEncoder().withoutPadding().encodeToString(DeviceKey.rawPublicKey(pair)));
        Map<String, Object> enroll = api.post("/api/devices/enroll", req, null);

        String deviceCode = string(enroll, "device_code");
        String userCode = string(enroll, "user_code");
        String verificationUrl = string(enroll, "verification_url");
        long interval = number(enroll, "interval", 5);
        long expiresIn = number(enroll, "expires_in", 600);

        out.accept("Open " + verificationUrl + "?code=" + userCode + " and approve code " + userCode);

        long deadline = System.currentTimeMillis() + expiresIn * 1000;
        Map<String, Object> poll = Map.of("device_code", deviceCode);
        while (true) {
            sleep(interval);
            try {
                Map<String, Object> answer = api.post("/api/devices/enroll/poll", poll, null);
                Object id = answer.get("device_id");
                if (!(id instanceof Long deviceId)) {
                    throw new IOException("the enrollment answer has no device_id");
                }
                Path saved = DeviceKey.of(server, deviceId, pair).save(server);
                out.accept("Approved. This machine is device " + deviceId + "; its key is in " + saved);
                return saved;
            } catch (ApiException e) {
                switch (e.error()) {
                    case "authorization_pending" -> {
                    }
                    case "slow_down" -> interval += 5;
                    case "expired_token" -> throw new IOException(
                            "the code " + userCode + " expired before anyone approved it; run the login again");
                    default -> throw e;
                }
            }
            if (System.currentTimeMillis() > deadline) {
                throw new IOException("the code " + userCode + " expired before anyone approved it; run the login again");
            }
        }
    }

    private static void sleep(long seconds) throws InterruptedIOException {
        try {
            Thread.sleep(seconds * 1000);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new InterruptedIOException("enrollment interrupted");
        }
    }

    private static String string(Map<String, Object> m, String key) throws IOException {
        if (m.get(key) instanceof String s && !s.isEmpty()) {
            return s;
        }
        throw new IOException("the enrollment answer has no " + key);
    }

    private static long number(Map<String, Object> m, String key, long fallback) {
        return m.get(key) instanceof Long n && n > 0 ? n : fallback;
    }
}
