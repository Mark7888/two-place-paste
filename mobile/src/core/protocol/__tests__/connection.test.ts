import { EntryUploadChunkRequest } from '../../../protocol/gen/tpp/v1/entry';
import { Envelope, MessageType } from '../../../protocol/gen/tpp/v1/envelope';
import { concat } from '../../bytes';
import { fieldHead } from '../connection';

describe('fieldHead', () => {
  // A spliced frame is the generated encoding up to its last field, then
  // fieldHead, then the bytes: it must be exactly what the codec writes.
  test.each([1, 127, 128, 300, 16383, 16384, (1 << 20) + 7])(
    'heads a %i-byte last field exactly as the generated codec does',
    (n) => {
      const data = new Uint8Array(n).fill(9);
      const whole = EntryUploadChunkRequest.encode({ uploadHandle: 'h', index: 4, data }).finish();
      const head = concat(EntryUploadChunkRequest.encode({ uploadHandle: 'h', index: 4, data: new Uint8Array(0) }).finish(), fieldHead(3, n));
      expect(Buffer.from(concat(head, data)).equals(Buffer.from(whole))).toBe(true);
    },
  );

  test('heads an envelope payload the same way', () => {
    const payload = new Uint8Array(200).fill(1);
    const type = MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST;
    const whole = Envelope.encode({ id: 'abc', type, payload }).finish();
    const head = concat(Envelope.encode({ id: 'abc', type, payload: new Uint8Array(0) }).finish(), fieldHead(3, payload.length));
    expect(Buffer.from(concat(head, payload)).equals(Buffer.from(whole))).toBe(true);
  });
});
