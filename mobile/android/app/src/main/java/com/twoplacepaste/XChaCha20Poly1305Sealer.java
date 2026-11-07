package com.twoplacepaste;

import java.util.Arrays;

/**
 * Streaming XChaCha20-Poly1305 encryption (draft-irtf-cfrg-xchacha, RFC 8439
 * §2.8): the AEAD of spec/crypto.md §5.2, done here so a large file shared to
 * the app is sealed at native speed rather than in JavaScript, which on the
 * phone runs with no JIT and took the bulk of an upload's time.
 *
 * It is written out rather than taken from the platform because the platform's
 * ChaCha20-Poly1305 (Android 9+ only) buffers the whole input before it
 * encrypts, on a Java heap of a few hundred megabytes; this one streams, with
 * constant memory, on every Android version the app runs on.
 *
 * Only sealing is here, and it is never trusted blind: the JavaScript side
 * seals test inputs both here and with `@noble/ciphers` and uses this one only
 * when every byte agrees (`nativeSeal.ts`). It is also held to the RFC 8439
 * and XChaCha test vectors in `scripts/aead-test` beside this file's tests.
 *
 * Usage: construct with the key, the 24-byte nonce and the associated data;
 * call {@link #update} with the plaintext in pieces of any size, in order;
 * then {@link #finish} returns the 16-byte tag that follows the ciphertext.
 */
final class XChaCha20Poly1305Sealer {

    static final int KEY_SIZE = 32;
    static final int NONCE_SIZE = 24;
    static final int TAG_SIZE = 16;

    private final int[] state = new int[16];
    private final int[] working = new int[16];
    private final byte[] keystream = new byte[64];
    /** keystreamUsed is how much of the current keystream block has been used; 64 means none is left. */
    private int keystreamUsed = 64;

    private final Poly1305 mac;
    private final long aadLength;
    private long dataLength;
    private boolean finished;

    XChaCha20Poly1305Sealer(byte[] key, byte[] nonce, byte[] aad) {
        if (key.length != KEY_SIZE) {
            throw new IllegalArgumentException("key is " + key.length + " bytes, want " + KEY_SIZE);
        }
        if (nonce.length != NONCE_SIZE) {
            throw new IllegalArgumentException("nonce is " + nonce.length + " bytes, want " + NONCE_SIZE);
        }
        // XChaCha20: HChaCha20 over the key and the first 16 nonce bytes gives
        // the subkey; the last 8 nonce bytes, after four zero bytes, are the
        // 96-bit ChaCha20 nonce.
        byte[] subkey = hchacha20(key, nonce);
        setState(subkey, nonce);
        Arrays.fill(subkey, (byte) 0);

        // The Poly1305 key is the first 32 bytes of keystream block 0; the
        // ciphertext starts at block 1.
        state[12] = 0;
        block();
        mac = new Poly1305(Arrays.copyOf(keystream, 32));
        state[12] = 1;
        keystreamUsed = 64;

        mac.update(aad, 0, aad.length);
        mac.pad();
        aadLength = aad.length;
    }

    /** update encrypts {@code len} bytes of {@code in} into {@code out}, which may be the same array. */
    void update(byte[] in, int inOff, int len, byte[] out, int outOff) {
        if (finished) {
            throw new IllegalStateException("the sealer has already finished");
        }
        int done = 0;
        while (done < len) {
            if (keystreamUsed == 64) {
                block();
                state[12]++;
                if (state[12] == 0) {
                    // 2^32 blocks of 64 bytes is 256 GiB: never an entry.
                    throw new IllegalStateException("the block counter ran out");
                }
                keystreamUsed = 0;
            }
            int n = Math.min(64 - keystreamUsed, len - done);
            for (int i = 0; i < n; i++) {
                out[outOff + done + i] = (byte) (in[inOff + done + i] ^ keystream[keystreamUsed + i]);
            }
            mac.update(out, outOff + done, n);
            keystreamUsed += n;
            done += n;
        }
        dataLength += len;
    }

    /** finish returns the tag. The sealer cannot be used after it. */
    byte[] finish() {
        if (finished) {
            throw new IllegalStateException("the sealer has already finished");
        }
        finished = true;
        mac.pad();
        byte[] lengths = new byte[16];
        putLongLE(lengths, 0, aadLength);
        putLongLE(lengths, 8, dataLength);
        mac.update(lengths, 0, 16);
        Arrays.fill(state, 0);
        Arrays.fill(keystream, (byte) 0);
        return mac.finish();
    }

    private void setState(byte[] subkey, byte[] nonce) {
        state[0] = 0x61707865;
        state[1] = 0x3320646e;
        state[2] = 0x79622d32;
        state[3] = 0x6b206574;
        for (int i = 0; i < 8; i++) {
            state[4 + i] = intLE(subkey, i * 4);
        }
        state[12] = 0;
        state[13] = 0;
        state[14] = intLE(nonce, 16);
        state[15] = intLE(nonce, 20);
    }

    /** block writes the keystream block for the current counter into {@link #keystream}. */
    private void block() {
        System.arraycopy(state, 0, working, 0, 16);
        rounds(working);
        for (int i = 0; i < 16; i++) {
            putIntLE(keystream, i * 4, working[i] + state[i]);
        }
    }

    static byte[] hchacha20(byte[] key, byte[] nonce) {
        int[] x = new int[16];
        x[0] = 0x61707865;
        x[1] = 0x3320646e;
        x[2] = 0x79622d32;
        x[3] = 0x6b206574;
        for (int i = 0; i < 8; i++) {
            x[4 + i] = intLE(key, i * 4);
        }
        for (int i = 0; i < 4; i++) {
            x[12 + i] = intLE(nonce, i * 4);
        }
        rounds(x);
        byte[] out = new byte[32];
        for (int i = 0; i < 4; i++) {
            putIntLE(out, i * 4, x[i]);
            putIntLE(out, 16 + i * 4, x[12 + i]);
        }
        Arrays.fill(x, 0);
        return out;
    }

    /** rounds is the 20 ChaCha rounds: ten double rounds of column and diagonal quarter rounds. */
    private static void rounds(int[] x) {
        for (int i = 0; i < 10; i++) {
            quarter(x, 0, 4, 8, 12);
            quarter(x, 1, 5, 9, 13);
            quarter(x, 2, 6, 10, 14);
            quarter(x, 3, 7, 11, 15);
            quarter(x, 0, 5, 10, 15);
            quarter(x, 1, 6, 11, 12);
            quarter(x, 2, 7, 8, 13);
            quarter(x, 3, 4, 9, 14);
        }
    }

    private static void quarter(int[] x, int a, int b, int c, int d) {
        x[a] += x[b];
        x[d] = Integer.rotateLeft(x[d] ^ x[a], 16);
        x[c] += x[d];
        x[b] = Integer.rotateLeft(x[b] ^ x[c], 12);
        x[a] += x[b];
        x[d] = Integer.rotateLeft(x[d] ^ x[a], 8);
        x[c] += x[d];
        x[b] = Integer.rotateLeft(x[b] ^ x[c], 7);
    }

    static int intLE(byte[] b, int off) {
        return (b[off] & 0xff) | (b[off + 1] & 0xff) << 8 | (b[off + 2] & 0xff) << 16 | (b[off + 3] & 0xff) << 24;
    }

    static void putIntLE(byte[] b, int off, int v) {
        b[off] = (byte) v;
        b[off + 1] = (byte) (v >>> 8);
        b[off + 2] = (byte) (v >>> 16);
        b[off + 3] = (byte) (v >>> 24);
    }

    static void putLongLE(byte[] b, int off, long v) {
        for (int i = 0; i < 8; i++) {
            b[off + i] = (byte) (v >>> (8 * i));
        }
    }

    /**
     * Poly1305 (RFC 8439 §2.5), in five 26-bit limbs with 64-bit products —
     * the "donna-32" layout — and a 16-byte buffer, so input can come in
     * pieces of any size. {@link #pad} zero-fills to a 16-byte boundary, which
     * is all the AEAD construction pads.
     */
    static final class Poly1305 {
        private final long r0, r1, r2, r3, r4;
        private final long s1, s2, s3, s4;
        private final int pad0, pad1, pad2, pad3;
        private long h0, h1, h2, h3, h4;
        private final byte[] buffer = new byte[16];
        private int buffered;

        Poly1305(byte[] key) {
            // r is clamped as RFC 8439 §2.5 says, then split into 26-bit limbs.
            r0 = intLE(key, 0) & 0x3ffffff;
            r1 = (intLE(key, 3) >>> 2) & 0x3ffff03;
            r2 = (intLE(key, 6) >>> 4) & 0x3ffc0ff;
            r3 = (intLE(key, 9) >>> 6) & 0x3f03fff;
            r4 = (intLE(key, 12) >>> 8) & 0x00fffff;
            s1 = r1 * 5;
            s2 = r2 * 5;
            s3 = r3 * 5;
            s4 = r4 * 5;
            pad0 = intLE(key, 16);
            pad1 = intLE(key, 20);
            pad2 = intLE(key, 24);
            pad3 = intLE(key, 28);
        }

        void update(byte[] m, int off, int len) {
            if (buffered > 0) {
                int n = Math.min(16 - buffered, len);
                System.arraycopy(m, off, buffer, buffered, n);
                buffered += n;
                off += n;
                len -= n;
                if (buffered < 16) {
                    return;
                }
                blocks(buffer, 0, 16, 1L << 24);
                buffered = 0;
            }
            int whole = len & ~15;
            if (whole > 0) {
                blocks(m, off, whole, 1L << 24);
                off += whole;
                len -= whole;
            }
            if (len > 0) {
                System.arraycopy(m, off, buffer, 0, len);
                buffered = len;
            }
        }

        /** pad completes the current 16-byte block with zeros, if one is begun. */
        void pad() {
            if (buffered > 0) {
                Arrays.fill(buffer, buffered, 16, (byte) 0);
                blocks(buffer, 0, 16, 1L << 24);
                buffered = 0;
            }
        }

        /** blocks absorbs whole 16-byte blocks; {@code hibit} is the 2^128 bit every full block carries. */
        private void blocks(byte[] m, int off, int len, long hibit) {
            long a0 = h0, a1 = h1, a2 = h2, a3 = h3, a4 = h4;
            for (int end = off + len; off < end; off += 16) {
                a0 += intLE(m, off) & 0x3ffffffL;
                a1 += (intLE(m, off + 3) >>> 2) & 0x3ffffffL;
                a2 += (intLE(m, off + 6) >>> 4) & 0x3ffffffL;
                a3 += (intLE(m, off + 9) >>> 6) & 0x3ffffffL;
                a4 += ((intLE(m, off + 12) >>> 8) & 0xffffffL) | hibit;

                long d0 = a0 * r0 + a1 * s4 + a2 * s3 + a3 * s2 + a4 * s1;
                long d1 = a0 * r1 + a1 * r0 + a2 * s4 + a3 * s3 + a4 * s2;
                long d2 = a0 * r2 + a1 * r1 + a2 * r0 + a3 * s4 + a4 * s3;
                long d3 = a0 * r3 + a1 * r2 + a2 * r1 + a3 * r0 + a4 * s4;
                long d4 = a0 * r4 + a1 * r3 + a2 * r2 + a3 * r1 + a4 * r0;

                long c = d0 >>> 26;
                a0 = d0 & 0x3ffffff;
                d1 += c;
                c = d1 >>> 26;
                a1 = d1 & 0x3ffffff;
                d2 += c;
                c = d2 >>> 26;
                a2 = d2 & 0x3ffffff;
                d3 += c;
                c = d3 >>> 26;
                a3 = d3 & 0x3ffffff;
                d4 += c;
                c = d4 >>> 26;
                a4 = d4 & 0x3ffffff;
                a0 += c * 5;
                c = a0 >>> 26;
                a0 &= 0x3ffffff;
                a1 += c;
            }
            h0 = a0;
            h1 = a1;
            h2 = a2;
            h3 = a3;
            h4 = a4;
        }

        /** finish returns the 16-byte tag: h fully reduced mod 2^130-5, plus the pad, mod 2^128. */
        byte[] finish() {
            pad();
            long a0 = h0, a1 = h1, a2 = h2, a3 = h3, a4 = h4;
            long c = a1 >>> 26;
            a1 &= 0x3ffffff;
            a2 += c;
            c = a2 >>> 26;
            a2 &= 0x3ffffff;
            a3 += c;
            c = a3 >>> 26;
            a3 &= 0x3ffffff;
            a4 += c;
            c = a4 >>> 26;
            a4 &= 0x3ffffff;
            a0 += c * 5;
            c = a0 >>> 26;
            a0 &= 0x3ffffff;
            a1 += c;

            // g = h + 5 - 2^130; take g when it did not go negative.
            long g0 = a0 + 5;
            c = g0 >>> 26;
            g0 &= 0x3ffffff;
            long g1 = a1 + c;
            c = g1 >>> 26;
            g1 &= 0x3ffffff;
            long g2 = a2 + c;
            c = g2 >>> 26;
            g2 &= 0x3ffffff;
            long g3 = a3 + c;
            c = g3 >>> 26;
            g3 &= 0x3ffffff;
            long g4 = a4 + c - (1L << 26);

            // mask is all ones when g4 is non-negative (h >= p), else zero.
            // The shift is unsigned: the sign bit alone, as 1 or 0.
            long mask = (g4 >>> 63) - 1;
            a0 = (a0 & ~mask) | (g0 & mask);
            a1 = (a1 & ~mask) | (g1 & mask);
            a2 = (a2 & ~mask) | (g2 & mask);
            a3 = (a3 & ~mask) | (g3 & mask);
            a4 = (a4 & ~mask) | (g4 & mask);

            // h as four 32-bit words, plus the pad, with carries.
            long w0 = (a0 | (a1 << 26)) & 0xffffffffL;
            long w1 = ((a1 >>> 6) | (a2 << 20)) & 0xffffffffL;
            long w2 = ((a2 >>> 12) | (a3 << 14)) & 0xffffffffL;
            long w3 = ((a3 >>> 18) | (a4 << 8)) & 0xffffffffL;

            long f = w0 + (pad0 & 0xffffffffL);
            byte[] tag = new byte[16];
            putIntLE(tag, 0, (int) f);
            f = w1 + (pad1 & 0xffffffffL) + (f >>> 32);
            putIntLE(tag, 4, (int) f);
            f = w2 + (pad2 & 0xffffffffL) + (f >>> 32);
            putIntLE(tag, 8, (int) f);
            f = w3 + (pad3 & 0xffffffffL) + (f >>> 32);
            putIntLE(tag, 12, (int) f);
            h0 = h1 = h2 = h3 = h4 = 0;
            return tag;
        }
    }
}
