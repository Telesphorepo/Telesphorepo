package com.telesphorepo.maven.client;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileSystems;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.attribute.AclEntry;
import java.nio.file.attribute.AclEntryPermission;
import java.nio.file.attribute.AclEntryType;
import java.nio.file.attribute.AclFileAttributeView;
import java.nio.file.attribute.PosixFilePermissions;
import java.nio.file.attribute.UserPrincipal;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PrivateKey;
import java.security.Signature;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.Arrays;
import java.util.Base64;
import java.util.EnumSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * An enrolled device: an Ed25519 key pair and the device id the server gave it. It lives in
 * {@code ~/.telesphorepo/<host>/device.json}, readable by its owner only:
 * <pre>{"server": "https://repo.acme.com", "device_id": 7, "private_key": "&lt;base64 PKCS#8&gt;",
 *  "public_key": "&lt;base64 raw 32 bytes&gt;", "created_at": 1760400000}</pre>
 * The Gradle plugin and the Maven extension share this file.
 */
public final class DeviceKey {
    /** The prefix of the signed text, see {@link #signPublish}. */
    public static final String SIGN_PREFIX = "telesphorepo publish v1\n";

    private final String server;
    private final long deviceId;
    private final PrivateKey privateKey;
    private final byte[] publicKey;
    private final long createdAt;

    private DeviceKey(String server, long deviceId, PrivateKey privateKey, byte[] publicKey, long createdAt) {
        this.server = server;
        this.deviceId = deviceId;
        this.privateKey = privateKey;
        this.publicKey = publicKey;
        this.createdAt = createdAt;
    }

    /** Makes a new Ed25519 key pair. */
    public static KeyPair generate() {
        try {
            return KeyPairGenerator.getInstance("Ed25519").generateKeyPair();
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("this JDK has no Ed25519", e);
        }
    }

    /** The raw 32-byte public key: the last 32 bytes of its X.509 encoding. */
    public static byte[] rawPublicKey(KeyPair pair) {
        byte[] x509 = pair.getPublic().getEncoded();
        return Arrays.copyOfRange(x509, x509.length - 32, x509.length);
    }

    /** A device for {@code server} made from an approved key pair. */
    public static DeviceKey of(ServerUrl server, long deviceId, KeyPair pair) {
        return new DeviceKey(server.base(), deviceId, pair.getPrivate(), rawPublicKey(pair),
                System.currentTimeMillis() / 1000);
    }

    /** {@code ~/.telesphorepo/<host>/device.json}. */
    public static Path file(ServerUrl server) {
        return Path.of(System.getProperty("user.home"), ".telesphorepo", server.hostDir(), "device.json");
    }

    /**
     * Reads the device for {@code server}, or returns null when there is none. Only
     * {@code device_id} and {@code private_key} are needed; the other fields are informative.
     */
    public static DeviceKey load(ServerUrl server) throws IOException {
        Path f = file(server);
        if (!Files.isRegularFile(f)) {
            return null;
        }
        Map<String, Object> m = Json.parseObject(Files.readString(f, StandardCharsets.UTF_8));
        if (!(m.get("device_id") instanceof Long id) || id <= 0 || !(m.get("private_key") instanceof String p)) {
            throw new IOException(f + " is not a valid device file");
        }
        try {
            PrivateKey key = KeyFactory.getInstance("Ed25519")
                    .generatePrivate(new PKCS8EncodedKeySpec(Base64.getDecoder().decode(p)));
            byte[] pub = m.get("public_key") instanceof String k ? Base64.getDecoder().decode(k) : new byte[0];
            long created = m.get("created_at") instanceof Long c ? c : 0;
            return new DeviceKey(server.base(), id, key, pub, created);
        } catch (GeneralSecurityException | IllegalArgumentException e) {
            throw new IOException(f + " holds an invalid key", e);
        }
    }

    /** Writes the device file. Only the owner can read it; an old file is replaced at once. */
    public Path save(ServerUrl server) throws IOException {
        Path f = file(server);
        Path dir = f.getParent();
        Files.createDirectories(dir);
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("server", this.server);
        m.put("device_id", deviceId);
        m.put("private_key", Base64.getEncoder().encodeToString(privateKey.getEncoded()));
        m.put("public_key", Base64.getEncoder().encodeToString(publicKey));
        m.put("created_at", createdAt);
        Path tmp = dir.resolve("device.json.tmp");
        Files.deleteIfExists(tmp);
        boolean posix = FileSystems.getDefault().supportedFileAttributeViews().contains("posix");
        if (posix) {
            Files.setPosixFilePermissions(dir, PosixFilePermissions.fromString("rwx------"));
            Files.createFile(tmp, PosixFilePermissions.asFileAttribute(PosixFilePermissions.fromString("rw-------")));
        } else {
            Files.createFile(tmp);
            ownerOnly(tmp);
        }
        Files.writeString(tmp, Json.write(m) + "\n", StandardCharsets.UTF_8);
        Files.move(tmp, f, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        return f;
    }

    /**
     * Signs a publish-session request: Ed25519 over
     * {@code "telesphorepo publish v1\n" + nonce + "\n" + device_id + "\n" + repo}, as unpadded
     * base64url.
     */
    public String signPublish(String nonce, String repo) {
        String text = SIGN_PREFIX + nonce + "\n" + deviceId + "\n" + repo;
        try {
            Signature sig = Signature.getInstance("Ed25519");
            sig.initSign(privateKey);
            sig.update(text.getBytes(StandardCharsets.UTF_8));
            return Base64.getUrlEncoder().withoutPadding().encodeToString(sig.sign());
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("cannot sign with the device key", e);
        }
    }

    public long deviceId() {
        return deviceId;
    }

    public long createdAt() {
        return createdAt;
    }

    private static void ownerOnly(Path p) throws IOException {
        AclFileAttributeView view = Files.getFileAttributeView(p, AclFileAttributeView.class);
        if (view == null) {
            return;
        }
        UserPrincipal owner = Files.getOwner(p);
        AclEntry entry = AclEntry.newBuilder()
                .setType(AclEntryType.ALLOW)
                .setPrincipal(owner)
                .setPermissions(EnumSet.allOf(AclEntryPermission.class))
                .build();
        view.setAcl(List.of(entry));
    }
}
