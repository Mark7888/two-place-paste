/**
 * One stylesheet for the whole app. Five screens do not need a design system,
 * and a dependency that would provide one is a dependency to update.
 */

import { StyleSheet } from 'react-native';

export const colors = {
  background: '#101418',
  surface: '#182029',
  border: '#26313d',
  text: '#e8eef4',
  muted: '#93a2b1',
  accent: '#4a9df8',
  danger: '#f2705a',
  ok: '#5bc98d',
};

export const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.background },
  content: { padding: 16, gap: 12 },
  title: { color: colors.text, fontSize: 22, fontWeight: '600' },
  heading: { color: colors.text, fontSize: 16, fontWeight: '600' },
  text: { color: colors.text, fontSize: 15 },
  muted: { color: colors.muted, fontSize: 13 },
  mono: { color: colors.text, fontFamily: 'monospace', fontSize: 12 },
  card: {
    backgroundColor: colors.surface,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    padding: 14,
    gap: 8,
  },
  button: {
    backgroundColor: colors.accent,
    borderRadius: 10,
    paddingVertical: 13,
    paddingHorizontal: 16,
    alignItems: 'center',
  },
  buttonSecondary: {
    backgroundColor: 'transparent',
    borderColor: colors.border,
    borderWidth: 1,
  },
  buttonDanger: { backgroundColor: colors.danger },
  buttonDisabled: { opacity: 0.45 },
  buttonLabel: { color: '#08121c', fontSize: 15, fontWeight: '600' },
  buttonLabelSecondary: { color: colors.text },
  input: {
    backgroundColor: colors.surface,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 10,
    color: colors.text,
    padding: 12,
    fontSize: 14,
  },
  tabBar: {
    flexDirection: 'row',
    borderTopColor: colors.border,
    borderTopWidth: 1,
    backgroundColor: colors.surface,
  },
  tab: { flex: 1, paddingVertical: 12, alignItems: 'center' },
  tabLabel: { color: colors.muted, fontSize: 12 },
  tabLabelActive: { color: colors.accent, fontWeight: '600' },
  row: { flexDirection: 'row', gap: 10, alignItems: 'center' },
  status: { paddingHorizontal: 16, paddingVertical: 8 },
});
