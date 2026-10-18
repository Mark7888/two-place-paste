import { describeHeader, formatSize, headerPreviewable, transferText, kindOf, previewOf, toBase64, PREVIEW_CHARS } from '../preview';
import { utf8, type Item } from '../../core';

function item(contentType: string, body: Uint8Array, filename = ''): Item {
  return { contentType, filename, body, createdAt: new Date(0), meta: null };
}

describe('kindOf', () => {
  // These three rules mirror the desktop's clipboard.Content.Kind exactly. If
  // they drift, the two clients describe the same entry differently.
  it('calls anything with a filename a file, whatever its media type', () => {
    expect(kindOf('image/png', 'shot.png')).toBe('file');
    expect(kindOf('text/plain', 'notes.txt')).toBe('file');
  });

  it('calls an image/* body an image', () => {
    expect(kindOf('image/png', '')).toBe('image');
    expect(kindOf('image/jpeg', '')).toBe('image');
  });

  it('calls everything else text', () => {
    expect(kindOf('text/plain; charset=utf-8', '')).toBe('text');
    expect(kindOf('application/octet-stream', '')).toBe('text');
  });
});

describe('previewOf', () => {
  it('renders a short text entry whole', () => {
    const p = previewOf(item('text/plain', utf8('hello there')));
    expect(p.kind).toBe('text');
    expect(p.text).toBe('hello there');
    expect(p.truncated).toBe(false);
    expect(p.contentType).toBe('text/plain');
  });

  it('bounds a long one and says so', () => {
    const p = previewOf(item('text/plain', utf8('a'.repeat(PREVIEW_CHARS + 50))));
    expect(p.truncated).toBe(true);
    expect(Array.from(p.text)).toHaveLength(PREVIEW_CHARS + 1); // plus the ellipsis
  });

  it('counts code points, so an emoji is never cut in half', () => {
    const p = previewOf(item('text/plain', utf8('😀'.repeat(PREVIEW_CHARS + 10))));
    expect(p.truncated).toBe(true);
    // Every character kept is a whole emoji, not half a surrogate pair.
    expect(p.text.slice(0, -1)).toBe('😀'.repeat(PREVIEW_CHARS));
  });

  it('gives a non-text body no text, rather than mojibake', () => {
    const p = previewOf(item('application/octet-stream', Uint8Array.of(0, 1, 2, 255)));
    expect(p.kind).toBe('text');
    expect(p.text).toBe('');
  });

  it('builds a data URI an <Image> can actually decode', () => {
    const png = Uint8Array.of(0x89, 0x50, 0x4e, 0x47);
    const p = previewOf(item('image/png', png));
    expect(p.kind).toBe('image');
    // Standard base64 with padding — not the wire format's unpadded base64url,
    // which an <Image> silently fails to decode.
    expect(p.imageUri).toBe('data:image/png;base64,iVBORw==');
    expect(p.imageTooLarge).toBe(false);
  });

  it('refuses to inline an image past the bound', () => {
    const p = previewOf(item('image/png', new Uint8Array((4 << 20) + 1)));
    expect(p.imageTooLarge).toBe(true);
    expect(p.imageUri).toBe('');
  });

  it('gives a file no body to draw', () => {
    const p = previewOf(item('application/pdf', Uint8Array.of(1, 2, 3), 'report.pdf'));
    expect(p.kind).toBe('file');
    expect(p.text).toBe('');
    expect(p.imageUri).toBe('');
    expect(p.filename).toBe('report.pdf');
  });
});

describe('toBase64', () => {
  it('pads to a multiple of four, at every remainder', () => {
    expect(toBase64(utf8(''))).toBe('');
    expect(toBase64(utf8('f'))).toBe('Zg==');
    expect(toBase64(utf8('fo'))).toBe('Zm8=');
    expect(toBase64(utf8('foo'))).toBe('Zm9v');
    expect(toBase64(utf8('foob'))).toBe('Zm9vYg==');
    expect(toBase64(utf8('fooba'))).toBe('Zm9vYmE=');
    expect(toBase64(utf8('foobar'))).toBe('Zm9vYmFy');
  });

  it('uses + and / rather than the url alphabet', () => {
    expect(toBase64(Uint8Array.of(0xfb, 0xff, 0xfe))).toBe('+//+');
  });
});

describe('headerPreviewable', () => {
  // The same rule as the desktop history: only text and images, read from the
  // entry's sealed header, are previewed; anything else is copy-only.
  const h = (contentType: string, filename = '') => ({ contentType, filename, size: 10 });

  it('previews unnamed text and images', () => {
    expect(headerPreviewable(h('text/plain; charset=utf-8'))).toBe(true);
    expect(headerPreviewable(h('image/png'))).toBe(true);
  });

  it('does not preview files, other types, or an unknown type', () => {
    expect(headerPreviewable(h('image/png', 'shot.png'))).toBe(false);
    expect(headerPreviewable(h('application/json'))).toBe(false);
    expect(headerPreviewable(null)).toBe(false);
  });

  it('names an entry by filename, then type', () => {
    expect(describeHeader(h('application/pdf', 'a.pdf'))).toBe('a.pdf');
    expect(describeHeader(h('text/plain'))).toBe('text/plain');
    expect(describeHeader(null)).toBe('type unknown');
  });
});

describe('formatSize', () => {
  it('keeps small sizes in bytes', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(512)).toBe('512 B');
    expect(formatSize(1023)).toBe('1023 B');
  });

  it('uses one decimal below ten of a unit and whole numbers above', () => {
    expect(formatSize(1024)).toBe('1.0 KB');
    expect(formatSize(3482)).toBe('3.4 KB');
    expect(formatSize(20 * 1024)).toBe('20 KB');
    expect(formatSize(1.5 * 1024 * 1024)).toBe('1.5 MB');
    expect(formatSize(50 * 1024 * 1024)).toBe('50 MB');
    expect(formatSize(3 * 1024 * 1024 * 1024)).toBe('3.0 GB');
  });

  it('never says 1024 of a unit', () => {
    expect(formatSize(1024 * 1024 - 1)).toBe('1.0 MB');
  });
});

describe('transferText', () => {
  const p = (over: Partial<Parameters<typeof transferText>[0]>) => ({
    entryId: 'e',
    direction: 'download' as const,
    done: 4 << 20,
    total: 20 << 20,
    filename: '',
    contentType: '',
    waiting: false,
    ...over,
  });

  it('names a file by its filename and says how far it is', () => {
    expect(transferText(p({ filename: 'report.pdf', contentType: 'application/pdf' }))).toEqual({
      title: 'Downloading report.pdf',
      detail: '4.0 MB of 20 MB · 20%',
      percent: 20,
    });
  });

  it('names an entry with no filename by its kind', () => {
    expect(transferText(p({ contentType: 'image/png', direction: 'upload' })).title).toBe('Uploading an image');
    expect(transferText(p({ contentType: 'text/plain; charset=utf-8' })).title).toBe('Downloading text');
    expect(transferText(p({ contentType: 'application/zip' })).title).toBe('Downloading application/zip');
    expect(transferText(p({})).title).toBe('Downloading a large entry');
  });

  it('does not reach 100% before the last byte', () => {
    expect(transferText(p({ done: (20 << 20) - 1 })).percent).toBe(99);
    expect(transferText(p({ done: 0, total: 0 })).percent).toBe(0);
  });
});

describe('transferText for a queued transfer', () => {
  it('says it is waiting, not how fast it is going', () => {
    const t = transferText({
      entryId: 'e',
      direction: 'download',
      done: 0,
      total: 20 << 20,
      filename: 'report.pdf',
      contentType: 'application/pdf',
      waiting: true,
    });
    expect(t.title).toBe('Waiting to download report.pdf');
    expect(t.detail).toBe('Queued behind another transfer · 20 MB');
  });
});
