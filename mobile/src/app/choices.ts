/**
 * When a received entry asks the user what to do with it, and what it offers.
 *
 * Text is copied: that is the whole point of a clipboard, and there is nothing
 * else worth doing with it here. A file the clipboard cannot hold always asks
 * — save it, open it or share it — because there is no default to fall back
 * on. An image sits between the two: it can be pasted, but it is just as
 * often something to keep or send on, so where the user is browsing (the
 * history, the store) it asks too, with Copy offered first. The one-tap paths
 * — Sync down and the quick-settings tile — still copy an image straight
 * away, because a second tap there is a cost every sync pays.
 *
 * Pure on purpose: it imports nothing from React Native, so it is tested in
 * Node like the rest of `src/app`'s logic.
 */

import type { PreviewKind } from './preview';

/** ChoiceRule says whether an entry asks first, and whether Copy is one of the answers. */
export interface ChoiceRule {
  /** ask is true when the user picks what happens, false when the entry is copied straight away. */
  ask: boolean;
  /** canCopy is true when Copy is offered among the answers. Only meaningful when `ask` is. */
  canCopy: boolean;
}

/** ChoiceContext is what the device can do, and where the entry is being received. */
export interface ChoiceContext {
  /** clipboardCanHold reports whether this device's clipboard can take the entry. */
  clipboardCanHold: boolean;
  /** filesAvailable reports whether this build can save, open and share files at all. */
  filesAvailable: boolean;
  /** askForImages is true where an image asks instead of being copied: the history and the store. */
  askForImages: boolean;
}

/** choiceRule decides, for an entry of the given kind, whether to ask and what to offer. */
export function choiceRule(kind: PreviewKind, ctx: ChoiceContext): ChoiceRule {
  if (!ctx.filesAvailable) {
    // Nothing to offer but the clipboard; a write it cannot take fails with
    // its own reason, as it always has.
    return { ask: false, canCopy: true };
  }
  if (!ctx.clipboardCanHold) {
    return { ask: true, canCopy: false };
  }
  if (kind === 'image' && ctx.askForImages) {
    return { ask: true, canCopy: true };
  }
  return { ask: false, canCopy: true };
}

/**
 * receiveLabel is the button that receives an entry in the history and the
 * store: what tapping it will do, so a file or an image is not mistaken for
 * something that lands straight on the clipboard.
 */
export function receiveLabel(rule: ChoiceRule): string {
  if (!rule.ask) {
    return 'Copy to this device';
  }
  return rule.canCopy ? 'Copy, save or share' : 'Save or open on this device';
}
