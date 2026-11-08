package com.twoplacepaste;

import java.security.MessageDigest;

/**
 * Streaming XChaCha20-Poly1305 decryption: the opening of spec/crypto.md
 * §5.4, done here so a large download is opened at native speed rather than
 * in JavaScript. It shares the keystream and Poly1305 of
 * {@link XChaCha20Poly1305Sealer}, and the same fences (see there).
 *
 * It is two passes, and the order is the point: {@link #authenticate} is fed
 * the whole ciphertext, {@link #verify} checks the tag, and only then will
 * {@link #decrypt} produce any plaintext. Nothing of a forged or damaged
 * container is ever decrypted, let alone written anywhere.
 */
final class XChaCha20Poly1305Opener {

    private final XChaCha20Poly1305Sealer.Keystream keystream;
    private final XChaCha20Poly1305Sealer.Poly1305 mac;
    private final long aadLength;
    private long authenticated;
    private long decrypted;
    private boolean checked;
    private boolean verified;

    XChaCha20Poly1305Opener(byte[] key, byte[] nonce, byte[] aad) {
        keystream = new XChaCha20Poly1305Sealer.Keystream(key, nonce);
        mac = new XChaCha20Poly1305Sealer.Poly1305(keystream.polyKey());
        mac.update(aad, 0, aad.length);
        mac.pad();
        aadLength = aad.length;
    }

    /** authenticate feeds the next {@code len} bytes of ciphertext to the tag check. */
    void authenticate(byte[] ciphertext, int off, int len) {
        if (checked) {
            throw new IllegalStateException("the tag has already been checked");
        }
        mac.update(ciphertext, off, len);
        authenticated += len;
    }

    /**
     * verify reports whether {@code tag} is the tag of everything authenticated,
     * comparing in constant time. It can be asked once.
     */
    boolean verify(byte[] tag) {
        if (checked) {
            throw new IllegalStateException("the tag has already been checked");
        }
        checked = true;
        byte[] want = XChaCha20Poly1305Sealer.finishMac(mac, aadLength, authenticated);
        verified = tag.length == XChaCha20Poly1305Sealer.TAG_SIZE && MessageDigest.isEqual(want, tag);
        if (!verified) {
            keystream.wipe();
        }
        return verified;
    }

    /**
     * decrypt turns the next {@code len} bytes of the ciphertext that was
     * verified into plaintext; {@code out} may be {@code in}. It refuses before
     * a successful {@link #verify}, and past what was authenticated.
     */
    void decrypt(byte[] in, int inOff, int len, byte[] out, int outOff) {
        if (!verified) {
            throw new IllegalStateException("the ciphertext has not been verified");
        }
        if (decrypted + len > authenticated) {
            throw new IllegalStateException("more ciphertext than was verified");
        }
        keystream.xor(in, inOff, len, out, outOff);
        decrypted += len;
        if (decrypted == authenticated) {
            keystream.wipe();
        }
    }
}
