package com.twoplacepaste;

import java.io.ByteArrayOutputStream;
import java.nio.charset.StandardCharsets;

/**
 * Splits a range reply from the relay into the bytes of the range and the
 * rest of the frame, so a large download never crosses into JavaScript.
 *
 * React Native hands every binary frame to JavaScript as base64, which the
 * engine there decodes slowly. For a range reply JavaScript asked to have
 * captured ({@code ShareModule.armCapture}), the frame is split here instead:
 * the reply's {@code data} field is written straight to a staged file, and
 * JavaScript receives the same frame without it — the envelope's id and type,
 * and the reply's metadata and offset, which it checks as before.
 *
 * Only what is needed is parsed, by protobuf's wire format alone: the
 * envelope ({@code tpp.v1.Envelope}: id = 1, type = 2, payload = 3) and one
 * length-delimited field of the payload. Every other field is copied through
 * byte for byte. A frame that does not parse, or that carries the field more
 * than once, is not split: it goes to JavaScript whole, as before.
 */
final class RangeCapture {

    private RangeCapture() {}

    /** Envelope is what is read of a frame's envelope. */
    static final class Envelope {
        final String id;
        final long type;
        final int payloadOffset;
        final int payloadLength;

        Envelope(String id, long type, int payloadOffset, int payloadLength) {
            this.id = id;
            this.type = type;
            this.payloadOffset = payloadOffset;
            this.payloadLength = payloadLength;
        }
    }

    /** Split is a frame with one payload field taken out, and where that field's bytes were. */
    static final class Split {
        final byte[] frame;
        final int dataOffset;
        final int dataLength;

        Split(byte[] frame, int dataOffset, int dataLength) {
            this.frame = frame;
            this.dataOffset = dataOffset;
            this.dataLength = dataLength;
        }
    }

    /** envelope reads a frame's envelope, or returns null when it does not parse. */
    static Envelope envelope(byte[] frame) {
        Reader r = new Reader(frame, 0, frame.length);
        String id = "";
        long type = 0;
        int payloadOffset = 0;
        int payloadLength = 0;
        try {
            while (r.more()) {
                long key = r.varint();
                int field = (int) (key >>> 3);
                int wire = (int) (key & 7);
                if (field == 1 && wire == 2) {
                    int n = r.length();
                    id = new String(frame, r.pos, n, StandardCharsets.UTF_8);
                    r.skip(n);
                } else if (field == 2 && wire == 0) {
                    type = r.varint();
                } else if (field == 3 && wire == 2) {
                    payloadLength = r.length();
                    payloadOffset = r.pos;
                    r.skip(payloadLength);
                } else {
                    r.skipField(wire);
                }
            }
        } catch (IllegalArgumentException e) {
            return null;
        }
        return new Envelope(id, type, payloadOffset, payloadLength);
    }

    /**
     * split takes length-delimited field {@code field} out of the frame's
     * payload, returning the frame without it — every other byte as it was —
     * and where the field's contents are in {@code frame}. It returns null
     * when the payload does not parse, or holds the field other than once.
     */
    static Split split(byte[] frame, Envelope env, int field) {
        try {
            // The payload, without the field.
            ByteArrayOutputStream payload = new ByteArrayOutputStream();
            Reader r = new Reader(frame, env.payloadOffset, env.payloadOffset + env.payloadLength);
            int dataOffset = -1;
            int dataLength = 0;
            while (r.more()) {
                int start = r.pos;
                long key = r.varint();
                int f = (int) (key >>> 3);
                int wire = (int) (key & 7);
                if (f == field && wire == 2) {
                    if (dataOffset >= 0) {
                        return null;
                    }
                    dataLength = r.length();
                    dataOffset = r.pos;
                    r.skip(dataLength);
                    continue;
                }
                r.skipField(wire);
                payload.write(frame, start, r.pos - start);
            }
            if (dataOffset < 0) {
                return null;
            }

            // The envelope, every field but the payload copied as it was, then
            // the new payload.
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            Reader e = new Reader(frame, 0, frame.length);
            while (e.more()) {
                int start = e.pos;
                long key = e.varint();
                int wire = (int) (key & 7);
                e.skipField(wire);
                if ((int) (key >>> 3) != 3) {
                    out.write(frame, start, e.pos - start);
                }
            }
            byte[] body = payload.toByteArray();
            if (body.length > 0) {
                writeVarint(out, (3L << 3) | 2);
                writeVarint(out, body.length);
                out.write(body, 0, body.length);
            }
            return new Split(out.toByteArray(), dataOffset, dataLength);
        } catch (IllegalArgumentException e) {
            return null;
        }
    }

    private static void writeVarint(ByteArrayOutputStream out, long v) {
        while ((v & ~0x7fL) != 0) {
            out.write((int) ((v & 0x7f) | 0x80));
            v >>>= 7;
        }
        out.write((int) v);
    }

    /** Reader walks protobuf's wire format within [pos, end), refusing anything malformed. */
    private static final class Reader {
        private final byte[] b;
        private final int end;
        int pos;

        Reader(byte[] b, int pos, int end) {
            if (pos < 0 || end > b.length || pos > end) {
                throw new IllegalArgumentException("out of range");
            }
            this.b = b;
            this.pos = pos;
            this.end = end;
        }

        boolean more() {
            return pos < end;
        }

        long varint() {
            long v = 0;
            for (int shift = 0; shift < 64; shift += 7) {
                if (pos >= end) {
                    throw new IllegalArgumentException("truncated varint");
                }
                int c = b[pos++] & 0xff;
                v |= (long) (c & 0x7f) << shift;
                if ((c & 0x80) == 0) {
                    return v;
                }
            }
            throw new IllegalArgumentException("varint too long");
        }

        int length() {
            long n = varint();
            if (n < 0 || n > end - pos) {
                throw new IllegalArgumentException("length past the end");
            }
            return (int) n;
        }

        void skip(int n) {
            if (n < 0 || n > end - pos) {
                throw new IllegalArgumentException("skip past the end");
            }
            pos += n;
        }

        void skipField(int wire) {
            switch (wire) {
                case 0:
                    varint();
                    break;
                case 1:
                    skip(8);
                    break;
                case 2:
                    skip(length());
                    break;
                case 5:
                    skip(4);
                    break;
                default:
                    // Groups (3, 4) are not in this schema; anything else is not protobuf.
                    throw new IllegalArgumentException("unsupported wire type " + wire);
            }
        }
    }
}
