import { splitNotices } from '../notices';

const FENCE = '='.repeat(80);

describe('splitNotices', () => {
  it('splits the overview from one piece per component', () => {
    const text = [
      'Title',
      '=====',
      '',
      'Overview.',
      '',
      FENCE,
      'react 19.2.3',
      'License: MIT',
      FENCE,
      '',
      'MIT License text.',
      '',
      FENCE,
      'qrcode 1.5.4',
      FENCE,
      '',
      'Another text.',
      '',
    ].join('\n');
    expect(splitNotices(text)).toEqual([
      'Title\n=====\n\nOverview.',
      'react 19.2.3\nLicense: MIT\n\nMIT License text.',
      'qrcode 1.5.4\n\nAnother text.',
    ]);
  });

  it('keeps a file with no components as its overview', () => {
    expect(splitNotices('Title\n\nNothing here.\n')).toEqual(['Title\n\nNothing here.']);
  });

  it('reads CRLF line endings the same', () => {
    const text = `Overview\r\n${FENCE}\r\nlib 1.0\r\n${FENCE}\r\n\r\ntext\r\n`;
    expect(splitNotices(text)).toEqual(['Overview', 'lib 1.0\n\ntext']);
  });
});
