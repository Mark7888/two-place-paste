/**
 * The operator link (SPEC §4.4). These are the cases the Go client tests
 * (pkg/tppclient/operator_test.go); a change here belongs there too.
 */

import { operatorLink, validOperatorURL } from '../urls';

const cases: { name: string; base: string; groupId: string; want: string | null }[] = [
  { name: 'no query', base: 'https://tpp.example.com/something', groupId: 'abc', want: 'https://tpp.example.com/something?group_id=abc' },
  { name: 'bare host', base: 'https://tpp.example.com', groupId: 'abc', want: 'https://tpp.example.com?group_id=abc' },
  { name: 'trailing slash', base: 'http://192.168.1.10:8080/', groupId: 'abc', want: 'http://192.168.1.10:8080/?group_id=abc' },
  { name: 'existing query', base: 'https://example.com/help?lang=en', groupId: 'abc', want: 'https://example.com/help?lang=en&group_id=abc' },
  { name: 'empty query', base: 'https://example.com/help?', groupId: 'abc', want: 'https://example.com/help?group_id=abc' },
  { name: 'query ending in &', base: 'https://example.com/help?lang=en&', groupId: 'abc', want: 'https://example.com/help?lang=en&group_id=abc' },
  { name: 'fragment kept last', base: 'https://example.com/help#contact', groupId: 'abc', want: 'https://example.com/help?group_id=abc#contact' },
  { name: 'query and fragment', base: 'https://example.com/help?lang=en#contact', groupId: 'abc', want: 'https://example.com/help?lang=en&group_id=abc#contact' },
  { name: 'base64url id unchanged', base: 'https://example.com/', groupId: 'hFps-imP_eNz9', want: 'https://example.com/?group_id=hFps-imP_eNz9' },
  { name: 'id escaped', base: 'https://example.com/', groupId: 'a b&c=d/é', want: 'https://example.com/?group_id=a%20b%26c%3Dd%2F%C3%A9' },
  { name: 'upper-case scheme', base: 'HTTPS://example.com/', groupId: 'abc', want: 'HTTPS://example.com/?group_id=abc' },
  { name: 'no group', base: 'https://example.com/', groupId: '', want: null },
  { name: 'empty url', base: '', groupId: 'abc', want: null },
  { name: 'bare host without scheme', base: 'tpp.example.com/something', groupId: 'abc', want: null },
  { name: 'javascript', base: 'javascript:alert(1)', groupId: 'abc', want: null },
  { name: 'intent', base: 'intent://scan/#Intent;scheme=zxing;end', groupId: 'abc', want: null },
  { name: 'file', base: 'file:///etc/passwd', groupId: 'abc', want: null },
  { name: 'no host', base: 'https://', groupId: 'abc', want: null },
  { name: 'no host before path', base: 'https:///path', groupId: 'abc', want: null },
  { name: 'port only', base: 'https://:8080/', groupId: 'abc', want: null },
  { name: 'credentials', base: 'https://user:pw@example.com/', groupId: 'abc', want: null },
  { name: 'space', base: 'https://example.com/a b', groupId: 'abc', want: null },
  { name: 'newline', base: 'https://example.com/\n', groupId: 'abc', want: null },
];

describe('operatorLink', () => {
  test.each(cases)('$name', ({ base, groupId, want }) => {
    expect(operatorLink(base, groupId)).toBe(want);
  });

  test('validOperatorURL agrees with whether a link is built', () => {
    for (const { base, want } of cases.filter((c) => c.groupId !== '')) {
      expect(validOperatorURL(base)).toBe(want !== null);
    }
  });
});
