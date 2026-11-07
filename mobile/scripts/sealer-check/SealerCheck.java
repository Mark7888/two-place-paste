package com.twoplacepaste;

import java.nio.file.*;
import java.util.*;

/**
 * Holds the app's native XChaCha20-Poly1305 sealer
 * (android/app/src/main/java/com/twoplacepaste/XChaCha20Poly1305Sealer.java)
 * to the cases gen.mjs writes: the spec's entry vectors and random inputs
 * sealed by @noble/ciphers. Each is sealed whole and twice more in random
 * pieces, since the sealer streams; one more is sealed in place, as the app
 * does. Run it with `npm run sealer-check` (needs a JDK).
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
    public static void main(String[] args) throws Exception {
        int ok = 0;
        int bad = 0;
        Random rnd = new Random(42);
        for (String file : args) {
            for (String line : Files.readAllLines(Paths.get(file))) {
                String[] f = line.split(" ");
                byte[] k = hex(f[0]), n = hex(f[1]), a = hex(f[2]), p = hex(f[3]), want = hex(f[4]);
                for (Random r : new Random[] {null, rnd, rnd}) {
                    if (Arrays.equals(seal(k, n, a, p, r), want)) ok++; else { bad++; System.out.println("MISMATCH len=" + p.length + " file=" + file); }
                }
            }
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
        System.out.println("ok=" + ok + " bad=" + bad);
        // Speed, for a sense of it: HotSpot here; ART on a phone is slower, but of this order.
        byte[] big = new byte[64 << 20];
        long t = System.nanoTime();
        XChaCha20Poly1305Sealer b = new XChaCha20Poly1305Sealer(k, n, new byte[0]);
        for (int off = 0; off < big.length; off += 65536) b.update(big, off, 65536, big, off);
        b.finish();
        double secs = (System.nanoTime() - t) / 1e9;
        System.out.printf("seal %.0f MB/s%n", 64 / secs);
        if (bad > 0) System.exit(1);
    }
}
