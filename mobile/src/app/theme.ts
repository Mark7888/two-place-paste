/**
 * One stylesheet for the whole app. Five screens do not need a design system,
 * and a dependency that would provide one is a dependency to update.
 *
 * The shape of it: a screen is a scrolling column of *sections*. A section is a
 * small upper-case heading and one surface holding its rows, separated by
 * hairlines. That replaces the previous card-per-thought layout, where four
 * paragraphs each got their own rounded border and the screen read as a stack
 * of unrelated notices.
 *
 * Sizes are the platform's, not invented: 48dp is Android's minimum touch
 * target and the tab bar is 60dp of it, so a tab is a target rather than a
 * label with some padding around it.
 */

import { StyleSheet } from 'react-native';

export const colors = {
  background: '#0e1216',
  surface: '#161d24',
  surfaceRaised: '#1c242d',
  border: '#232d38',
  hairline: '#1e2831',
  text: '#e8eef4',
  muted: '#93a2b1',
  faint: '#6d7c8c',
  accent: '#4a9df8',
  accentSoft: 'rgba(74, 157, 248, 0.14)',
  danger: '#f2705a',
  dangerSoft: 'rgba(242, 112, 90, 0.14)',
  ok: '#5bc98d',
  okSoft: 'rgba(91, 201, 141, 0.14)',
  scrim: 'rgba(4, 7, 10, 0.72)',
};

/** touchTarget is Android's minimum, and the floor for anything tappable. */
export const touchTarget = 48;

export const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.background },
  content: { padding: 16, paddingBottom: 32, gap: 18 },

  title: { color: colors.text, fontSize: 26, fontWeight: '700', letterSpacing: -0.4 },
  lede: { color: colors.muted, fontSize: 14, lineHeight: 20, marginTop: -8 },
  heading: { color: colors.text, fontSize: 16, fontWeight: '600' },
  sectionTitle: {
    color: colors.faint,
    fontSize: 11,
    fontWeight: '700',
    letterSpacing: 1,
    textTransform: 'uppercase',
    marginBottom: 8,
    marginLeft: 2,
  },
  text: { color: colors.text, fontSize: 15, lineHeight: 21 },
  muted: { color: colors.muted, fontSize: 13, lineHeight: 19 },
  small: { color: colors.muted, fontSize: 12, lineHeight: 17 },
  mono: { color: colors.text, fontFamily: 'monospace', fontSize: 12 },

  /** A section is a heading plus one surface. */
  section: { gap: 0 },
  surface: {
    backgroundColor: colors.surface,
    borderRadius: 14,
    overflow: 'hidden',
  },
  /** A row inside a surface. Rows are separated by one hairline, not by a
   *  border each. */
  row: {
    paddingHorizontal: 14,
    paddingVertical: 13,
    gap: 4,
    minHeight: touchTarget,
    justifyContent: 'center',
  },
  rowDivided: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: colors.border },
  rowInline: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  rowBody: { flex: 1, minWidth: 0, gap: 2 },

  /** card is kept for the few places that really are one standalone object. */
  card: {
    backgroundColor: colors.surface,
    borderRadius: 14,
    padding: 14,
    gap: 8,
  },

  button: {
    backgroundColor: colors.accent,
    borderRadius: 12,
    minHeight: touchTarget,
    paddingVertical: 13,
    paddingHorizontal: 16,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 8,
  },
  buttonSecondary: {
    backgroundColor: colors.surfaceRaised,
    borderColor: colors.border,
    borderWidth: 1,
  },
  buttonDanger: { backgroundColor: colors.danger },
  buttonDisabled: { opacity: 0.45 },
  buttonLabel: { color: '#08121c', fontSize: 15, fontWeight: '700' },
  buttonLabelSecondary: { color: colors.text },
  buttonLabelDanger: { color: '#1a0805' },

  input: {
    backgroundColor: colors.surfaceRaised,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    color: colors.text,
    padding: 13,
    fontSize: 14,
    minHeight: touchTarget,
  },

  /** The tab bar. Every measurement here is a touch target, not a label. */
  tabBar: {
    flexDirection: 'row',
    borderTopColor: colors.border,
    borderTopWidth: StyleSheet.hairlineWidth,
    backgroundColor: colors.surface,
  },
  tab: {
    flex: 1,
    minHeight: 60,
    paddingTop: 8,
    paddingBottom: 6,
    paddingHorizontal: 2,
    alignItems: 'center',
    justifyContent: 'center',
    gap: 3,
  },
  /** The active pill sits behind the icon, so "which tab am I on" survives
   *  being read at arm's length. */
  tabIconWrap: {
    width: 44,
    height: 26,
    borderRadius: 13,
    alignItems: 'center',
    justifyContent: 'center',
  },
  tabIconWrapActive: { backgroundColor: colors.accentSoft },
  tabLabel: { color: colors.muted, fontSize: 11, fontWeight: '500' },
  tabLabelActive: { color: colors.accent, fontWeight: '700' },

  /** A decrypted entry's body, shown in the history list. */
  previewBox: {
    backgroundColor: colors.background,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: 10,
    maxHeight: 220,
  },
  previewText: { color: colors.text, fontSize: 13, lineHeight: 19 },
  previewImage: {
    borderRadius: 10,
    maxHeight: 280,
    backgroundColor: colors.background,
  },

  inline: { flexDirection: 'row', gap: 10, alignItems: 'center' },
  status: { paddingHorizontal: 16, paddingVertical: 8 },

  /** A notice: one coloured block, used for outcomes and warnings alike. */
  notice: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: 8,
    borderRadius: 12,
    paddingHorizontal: 12,
    paddingVertical: 10,
  },
  noticeText: { flex: 1, fontSize: 13, lineHeight: 19 },

  /** The overlay window's root: the same scrim, filling its own window. */
  overlayRoot: {
    flex: 1,
    backgroundColor: colors.scrim,
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },

  /** The modal: a scrim, and a sheet centred over it. */
  scrim: {
    position: 'absolute',
    top: 0,
    right: 0,
    bottom: 0,
    left: 0,
    backgroundColor: colors.scrim,
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },
  sheet: {
    width: '100%',
    maxWidth: 420,
    backgroundColor: colors.surfaceRaised,
    borderRadius: 18,
    padding: 18,
    gap: 12,
  },
  sheetTitle: { color: colors.text, fontSize: 18, fontWeight: '700' },

  /** A choice in the direction dialog: a big target with its consequence
   *  spelled out, because neither of the two is the safe default. */
  choice: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    backgroundColor: colors.surface,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: colors.border,
    padding: 14,
    minHeight: 64,
  },
  choiceBody: { flex: 1, gap: 2 },
  choiceTitle: { color: colors.text, fontSize: 15, fontWeight: '700' },
  choiceWhy: { color: colors.muted, fontSize: 12, lineHeight: 17 },
});
