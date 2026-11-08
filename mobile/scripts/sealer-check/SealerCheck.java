package com.twoplacepaste;

import java.nio.file.*;
import java.util.*;

/**
 * Holds the app's native XChaCha20-Poly1305 sealer and opener
 * (android/app/src/main/java/com/twoplacepaste/) to the cases gen.mjs writes:
 * the spec's entry vectors and random inputs sealed by @noble/ciphers. Each
 * is sealed and opened whole and twice more in random pieces, since both
 * stream; each is also tampered with every way that matters, which the opener
 * must refuse. The native range capture (RangeCapture) is held to frames from
 * an independent encoder. Run it with `npm run sealer-check` (needs a JDK).
 */
public class SealerCheck {
    static byte[] hex(String s) {
        if (s.equals("-")) return new byte[0];
        byte[] b = new byte[s.length() / 2];
        for (int i = 0; i < b.length; i++) b[i] = (byte) Integer.parseInt(s.substring(2 * i, 2 * i + 2), 16);
        return b;
    }
    static byte[] seal(byte[] k, byte[] n, byte[] a, byte[] p, Random rnd) {
        XChaCha20Poly1305Sealer s = new XChaCha20Poly1305Sealer(k, n, a);
        byte[] out = new byte[p.length + 16];
        int off = 0;
        while (off < p.length) {
            int len = rnd == null ? p.length : Math.min(p.length - off, 1 + rnd.nextInt(rnd.nextBoolean() ? 70 : 70000));
            s.update(p, off, len, out, off);
            off += len;
        }
        System.arraycopy(s.finish(), 0, out, p.length, 16);
        return out;
    }
    /** open opens ciphertext-with-tag in pieces, or returns null when the tag does not verify. */
    static byte[] open(byte[] k, byte[] n, byte[] a, byte[] sealed, Random rnd) {
        int len = sealed.length - 16;
        XChaCha20Poly1305Opener o = new XChaCha20Poly1305Opener(k, n, a);
        for (int off = 0; off < len; ) {
            int piece = rnd == null ? len : Math.min(len - off, 1 + rnd.nextInt(70000));
            o.authenticate(sealed, off, piece);
            off += piece;
        }
        if (!o.verify(Arrays.copyOfRange(sealed, len, sealed.length))) {
            return null;
        }
        byte[] out = new byte[len];
        for (int off = 0; off < len; ) {
            int piece = rnd == null ? len : Math.min(len - off, 1 + rnd.nextInt(70000));
            o.decrypt(sealed, off, piece, out, off);
            off += piece;
        }
        return out;
    }

    public static void main(String[] args) throws Exception {
        int ok = 0;
        int bad = 0;
        Random rnd = new Random(42);
        for (String line : Files.readAllLines(Paths.get(args[0]))) {
            String[] f = line.split(" ");
            byte[] k = hex(f[0]), n = hex(f[1]), a = hex(f[2]), p = hex(f[3]), want = hex(f[4]);
            for (Random r : new Random[] {null, rnd, rnd}) {
                if (Arrays.equals(seal(k, n, a, p, r), want)) ok++; else { bad++; System.out.println("SEAL MISMATCH len=" + p.length); }
                if (Arrays.equals(open(k, n, a, want, r), p)) ok++; else { bad++; System.out.println("OPEN MISMATCH len=" + p.length); }
            }
            // Every kind of tampering is refused: a ciphertext byte, a tag
            // byte, the associated data, the nonce.
            byte[] flipped = want.clone();
            flipped[rnd.nextInt(flipped.length)] ^= 1;
            byte[] otherAad = Arrays.copyOf(a, a.length + 1);
            byte[] otherNonce = n.clone();
            otherNonce[0] ^= 1;
            for (byte[] refused : new byte[][] {open(k, n, a, flipped, rnd), open(k, n, otherAad, want, null), open(k, otherNonce, a, want, null)}) {
                if (refused == null) ok++; else { bad++; System.out.println("TAMPER ACCEPTED len=" + p.length); }
            }
        }
        // Nothing decrypts before the tag is checked.
        try {
            new XChaCha20Poly1305Opener(new byte[32], new byte[24], new byte[0]).decrypt(new byte[1], 0, 1, new byte[1], 0);
            bad++;
            System.out.println("DECRYPTED BEFORE VERIFY");
        } catch (IllegalStateException expected) {
            ok++;
        }
        // In place, as the file path uses one buffer for both.
        byte[] k = new byte[32], n = new byte[24], p = new byte[100000];
        rnd.nextBytes(k); rnd.nextBytes(n); rnd.nextBytes(p);
        byte[] want = seal(k, n, new byte[3], p, null);
        byte[] buf = p.clone();
        XChaCha20Poly1305Sealer s = new XChaCha20Poly1305Sealer(k, n, new byte[3]);
        s.update(buf, 0, 777, buf, 0); s.update(buf, 777, buf.length - 777, buf, 777);
        byte[] tag = s.finish();
        if (Arrays.equals(Arrays.copyOf(buf, buf.length), Arrays.copyOf(want, p.length)) && Arrays.equals(tag, Arrays.copyOfRange(want, p.length, p.length + 16))) ok++; else { bad++; System.out.println("MISMATCH in place"); }

        // Range capture: the frame without the payload's field 3, and that field's bytes.
        for (String line : Files.readAllLines(Paths.get(args[1]))) {
            String[] f = line.split(" ");
            byte[] frame = hex(f[0]);
            RangeCapture.Envelope env = RangeCapture.envelope(frame);
            RangeCapture.Split split = env == null ? null : RangeCapture.split(frame, env, 3);
            if (f[1].equals("null")) {
                if (split == null) ok++; else { bad++; System.out.println("SPLIT A FRAME TO LEAVE ALONE: " + f[0]); }
                continue;
            }
            byte[] wantFrame = hex(f[1]), wantData = hex(f[2]);
            if (split != null && Arrays.equals(split.frame, wantFrame)
                && Arrays.equals(Arrays.copyOfRange(frame, split.dataOffset, split.dataOffset + split.dataLength), wantData)) {
                ok++;
            } else {
                bad++;
                System.out.println("SPLIT MISMATCH: " + f[0].substring(0, Math.min(40, f[0].length())));
            }
        }

        System.out.println("ok=" + ok + " bad=" + bad);
        // Speed, for a sense of it: HotSpot here; ART on a phone is slower, but of this order.
        byte[] big = new byte[64 << 20];
        long t = System.nanoTime();
        XChaCha20Poly1305Sealer b = new XChaCha20Poly1305Sealer(k, n, new byte[0]);
        for (int off = 0; off < big.length; off += 65536) b.update(big, off, 65536, big, off);
        byte[] bigTag = b.finish();
        double secs = (System.nanoTime() - t) / 1e9;
        t = System.nanoTime();
        XChaCha20Poly1305Opener o = new XChaCha20Poly1305Opener(k, n, new byte[0]);
        for (int off = 0; off < big.length; off += 65536) o.authenticate(big, off, 65536);
        if (!o.verify(bigTag)) { bad++; System.out.println("BIG OPEN FAILED"); }
        for (int off = 0; off < big.length; off += 65536) o.decrypt(big, off, 65536, big, off);
        double openSecs = (System.nanoTime() - t) / 1e9;
        System.out.printf("seal %.0f MB/s, open %.0f MB/s%n", 64 / secs, 64 / openSecs);
        if (bad > 0) System.exit(1);
    }
}
