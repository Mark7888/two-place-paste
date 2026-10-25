import { StoreIndex, type StoreItem } from '../store';

function item(id: string, name: string, description: string, savedSec: number, rev: bigint, hash: string, ...tags: string[]): StoreItem {
  return {
    id,
    name,
    description,
    tags,
    savedAt: new Date(Date.UTC(2026, 9, 1, 12, 0, savedSec)),
    contentType: 'text/plain',
    filename: '',
    bodySize: 1,
    sha256Hex: hash,
    blobSize: 100,
    metaRev: rev,
    wrapEpoch: 1n,
    secrets: { contentKey: new Uint8Array(32), metaKey: new Uint8Array(32), contentId: id, contentEpoch: 1n },
  };
}

const ids = (items: StoreItem[]) => items.map((it) => it.id);

// The same cases as pkg/tppclient/store_index_test.go: the two indexes behave alike.
describe('StoreIndex', () => {
  const x = new StoreIndex();
  x.reset([
    item('a', 'Home wifi', 'The router in the hall', 1, 0n, 'w', 'home', 'network'),
    item('b', 'boiler manual', 'PDF from the installer', 2, 0n, 'm', 'home'),
    item('c', 'Work VPN', 'Config for the office router', 3, 0n, 'v', 'work', 'network'),
    item('d', 'Address', '', 3, 0n, 'addr'),
  ]);

  test.each([
    ['everything, newest first', {}, ['d', 'c', 'b', 'a']],
    ['oldest first', { sort: 'oldest' as const }, ['a', 'b', 'c', 'd']],
    ['by name, ignoring case', { sort: 'name' as const }, ['d', 'b', 'a', 'c']],
    ['by name, descending', { sort: 'name-desc' as const }, ['c', 'a', 'b', 'd']],
    ['one word in a description', { text: 'ROUTER' }, ['c', 'a']],
    ['every word must match', { text: 'router office' }, ['c']],
    ['a tag', { tags: ['home'] }, ['b', 'a']],
    ['every tag must match', { tags: ['home', 'network'] }, ['a']],
    ['text and tag', { text: 'router', tags: ['work'] }, ['c']],
    ['nothing matches', { text: 'nope' }, []],
  ])('%s', (_name, q, want) => {
    expect(ids(x.query(q))).toEqual(want);
  });
});

test('StoreIndex counts tags, finds duplicates and never rolls back a revision', () => {
  const x = new StoreIndex();
  x.put(item('a', 'Note', '', 1, 0n, 'same', 'home'));
  x.put(item('b', 'Copy', '', 2, 0n, 'same', 'home', 'work'));
  x.put(item('c', 'Other', '', 3, 0n, 'other'));

  expect(x.tags()).toEqual([
    { tag: 'home', count: 2 },
    { tag: 'work', count: 1 },
  ]);
  expect(ids(x.duplicates('same'))).toEqual(['b', 'a']);

  x.put(item('a', 'Renamed', '', 1, 2n, 'same'));
  x.put(item('a', 'Late event', '', 1, 1n, 'same'));
  expect(x.get('a')?.name).toBe('Renamed');
  x.remove('b');
  expect(x.size).toBe(2);
  expect(x.tags()).toEqual([]);
});
