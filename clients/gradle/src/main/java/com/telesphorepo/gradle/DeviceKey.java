package com.telesphorepo.gradle;

import org.gradle.api.GradleException;

import java.io.IOException;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardCopyOption;
import java.nio.file.attribute.AclEntry;
import java.nio.file.attribute.AclEntryPermission;
import java.nio.file.attribute.AclEntryType;
import java.nio.file.attribute.AclFileAttributeView;
import java.nio.file.attribute.PosixFilePermission;
import java.nio.file.attribute.PosixFilePermissions;
import java.nio.file.attribute.UserPrincipal;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PrivateKey;
import java.security.PublicKey;
import java.security.Signature;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.Arrays;
import java.util.Base64;
import java.util.EnumSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;

/**
 * The enrolled device key in {@code ~/.telesphorepo/<host>/device.json}. The file holds the
 * server URL, the device id and the Ed25519 private key (PKCS#8, base64), and only its owner can
 * read it: mode 600 on POSIX systems, an ACL with the owner as the only entry on Windows.
 */
final class DeviceKey {
    final long deviceId;
    private final PrivateKey key;

    private DeviceKey(long deviceId, PrivateKey key) {
        this.deviceId = deviceId;
        this.key = key;
    }

    /** The device file for the server at base. The directory is the host, plus "_port" when the URL names a port. */
    static Path file(URI base) {
        String host = base.getHost().toLowerCase(Locale.ROOT);
        if (base.getPort() != -1) {
            host += "_" + base.getPort();
        }
        return Paths.get(System.getProperty("user.home"), ".telesphorepo", host, "device.json");
    }

    static KeyPair generate() {
        try {
            return KeyPairGenerator.getInstance("Ed25519").generateKeyPair();
        } catch (GeneralSecurityException e) {
            throw new GradleException("This JDK cannot generate Ed25519 keys", e);
        }
    }

    /** The raw 32-byte Ed25519 public key, base64url without padding. */
    static String publicKey(PublicKey pub) {
        byte[] spki = pub.getEncoded(); // X.509 SubjectPublicKeyInfo: a 12-byte header, then the key
        byte[] raw = Arrays.copyOfRange(spki, spki.length - 32, spki.length);
        return Base64.getUrlEncoder().withoutPadding().encodeToString(raw);
    }

    /** Signs msg (UTF-8) and returns the signature as base64url without padding. */
    String sign(String msg) {
        try {
            Signature sig = Signature.getInstance("Ed25519");
            sig.initSign(key);
            sig.update(msg.getBytes(StandardCharsets.UTF_8));
            return Base64.getUrlEncoder().withoutPadding().encodeToString(sig.sign());
        } catch (GeneralSecurityException e) {
            throw new GradleException("Signing with the device key failed", e);
        }
    }

    /** Loads the device key for base, or returns null when there is none. */
    static DeviceKey load(URI base) {
        Path f = file(base);
        if (!Files.isRegularFile(f)) {
            return null;
        }
        try {
            if (posix(f)) {
                Set<PosixFilePermission> perms = Files.getPosixFilePermissions(f);
                for (PosixFilePermission p : perms) {
                    if (p.name().startsWith("GROUP") || p.name().startsWith("OTHERS")) {
                        throw new GradleException(f + " must be readable by its owner only. Run: chmod 600 " + f);
                    }
                }
            }
            Map<String, Object> m = Json.parseObject(new String(Files.readAllBytes(f), StandardCharsets.UTF_8));
            long id = Json.number(m, "device_id", 0);
            String pk = Json.string(m, "private_key");
            if (id <= 0 || pk == null) {
                throw new GradleException(f + " is not a valid device file. Run ./gradlew telesphorepoLogin");
            }
            PrivateKey key = KeyFactory.getInstance("Ed25519").generatePrivate(new PKCS8EncodedKeySpec(Base64.getDecoder().decode(pk)));
            return new DeviceKey(id, key);
        } catch (IOException | IllegalArgumentException | GeneralSecurityException e) {
            throw new GradleException("Cannot read " + f + ". Run ./gradlew telesphorepoLogin", e);
        }
    }

    /** Writes the device file for base atomically, with owner-only permissions. */
    static void save(URI base, long deviceId, PrivateKey key) {
        Path f = file(base);
        Path dir = f.getParent();
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("url", base.toString());
        m.put("device_id", deviceId);
        m.put("private_key", Base64.getEncoder().encodeToString(key.getEncoded()));
        byte[] data = (Json.write(m) + "\n").getBytes(StandardCharsets.UTF_8);
        Path tmp = dir.resolve("device.json.tmp");
        try {
            if (posix(f)) {
                Files.createDirectories(dir, PosixFilePermissions.asFileAttribute(PosixFilePermissions.fromString("rwx------")));
                Files.setPosixFilePermissions(dir, PosixFilePermissions.fromString("rwx------"));
                Files.deleteIfExists(tmp);
                Files.createFile(tmp, PosixFilePermissions.asFileAttribute(PosixFilePermissions.fromString("rw-------")));
            } else {
                Files.createDirectories(dir);
                ownerOnlyAcl(dir);
                Files.deleteIfExists(tmp);
                Files.createFile(tmp);
                ownerOnlyAcl(tmp);
            }
            Files.write(tmp, data);
            Files.move(tmp, f, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (IOException e) {
            throw new GradleException("Cannot write " + f, e);
        }
    }

    private static boolean posix(Path p) {
        return p.getFileSystem().supportedFileAttributeViews().contains("posix");
    }

    /** Replaces the ACL of p with one entry that gives its owner full control. */
    private static void ownerOnlyAcl(Path p) throws IOException {
        AclFileAttributeView view = Files.getFileAttributeView(p, AclFileAttributeView.class);
        if (view == null) {
            throw new IOException("the file system supports neither POSIX permissions nor ACLs");
        }
        UserPrincipal owner = view.getOwner();
        AclEntry entry = AclEntry.newBuilder()
            .setType(AclEntryType.ALLOW)
            .setPrincipal(owner)
            .setPermissions(EnumSet.allOf(AclEntryPermission.class))
            .build();
        view.setAcl(List.of(entry));
    }
}
