import { choiceRule, receiveLabel, type ChoiceContext } from '../choices';

const phone: ChoiceContext = { clipboardCanHold: true, filesAvailable: true, askForImages: false };

describe('choiceRule', () => {
  const cases: {
    name: string;
    kind: 'text' | 'image' | 'file';
    ctx: Partial<ChoiceContext>;
    want: { ask: boolean; canCopy: boolean };
  }[] = [
    { name: 'copies text', kind: 'text', ctx: {}, want: { ask: false, canCopy: true } },
    {
      name: 'copies text even where images ask',
      kind: 'text',
      ctx: { askForImages: true },
      want: { ask: false, canCopy: true },
    },
    {
      name: 'copies an image on the one-tap paths',
      kind: 'image',
      ctx: {},
      want: { ask: false, canCopy: true },
    },
    {
      name: 'asks for an image in the history and store, Copy included',
      kind: 'image',
      ctx: { askForImages: true },
      want: { ask: true, canCopy: true },
    },
    {
      name: 'asks for an image the clipboard cannot take, without Copy',
      kind: 'image',
      ctx: { askForImages: true, clipboardCanHold: false },
      want: { ask: true, canCopy: false },
    },
    {
      name: 'asks for a file, without Copy',
      kind: 'file',
      ctx: { clipboardCanHold: false },
      want: { ask: true, canCopy: false },
    },
    {
      name: 'asks for a body the clipboard cannot take, whatever its kind',
      kind: 'text',
      ctx: { clipboardCanHold: false },
      want: { ask: true, canCopy: false },
    },
    {
      name: 'never asks where files cannot be handled',
      kind: 'image',
      ctx: { askForImages: true, filesAvailable: false },
      want: { ask: false, canCopy: true },
    },
    {
      name: 'never asks for a file where files cannot be handled',
      kind: 'file',
      ctx: { clipboardCanHold: false, filesAvailable: false },
      want: { ask: false, canCopy: true },
    },
  ];

  it.each(cases)('$name', ({ kind, ctx, want }) => {
    expect(choiceRule(kind, { ...phone, ...ctx })).toEqual(want);
  });
});

describe('receiveLabel', () => {
  it.each([
    { rule: { ask: false, canCopy: true }, want: 'Copy to this device' },
    { rule: { ask: true, canCopy: true }, want: 'Copy, save or share' },
    { rule: { ask: true, canCopy: false }, want: 'Save or open on this device' },
  ])('labels $rule as "$want"', ({ rule, want }) => {
    expect(receiveLabel(rule)).toBe(want);
  });
});
