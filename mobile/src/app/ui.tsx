/** The controls every screen is built from. */

import React from 'react';
import {
  KeyboardAvoidingView,
  Modal,
  Pressable,
  ScrollView,
  Text,
  View,
} from 'react-native';

import { clipboard } from '../platform';

import { Icon, type IconName } from './icons';
import { failureMessage } from './session';
import { colors, styles, touchTarget } from './theme';

export function Button({
  label,
  onPress,
  disabled,
  variant = 'primary',
  icon,
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  variant?: 'primary' | 'secondary' | 'danger';
  icon?: IconName;
}): React.JSX.Element {
  const labelColor =
    variant === 'secondary'
      ? colors.text
      : variant === 'danger'
        ? '#1a0805'
        : '#08121c';
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled: disabled === true }}
      onPress={onPress}
      disabled={disabled}
      android_ripple={{ color: 'rgba(255,255,255,0.12)' }}
      style={[
        styles.button,
        variant === 'secondary' && styles.buttonSecondary,
        variant === 'danger' && styles.buttonDanger,
        disabled === true && styles.buttonDisabled,
      ]}
    >
      {icon !== undefined && <Icon name={icon} size={18} color={labelColor} />}
      <Text
        style={[
          styles.buttonLabel,
          variant === 'secondary' && styles.buttonLabelSecondary,
          variant === 'danger' && styles.buttonLabelDanger,
        ]}
      >
        {label}
      </Text>
    </Pressable>
  );
}

/**
 * Section is the screen's unit of grouping: a small heading over one surface.
 *
 * The rows inside it are separated by a hairline rather than each getting its
 * own border, which is what stops a settings screen from reading as a stack of
 * unrelated notices.
 */
export function Section({
  title,
  children,
}: {
  title?: string;
  children: React.ReactNode;
}): React.JSX.Element {
  const rows = React.Children.toArray(children).filter(Boolean);
  return (
    <View style={styles.section}>
      {title !== undefined && <Text style={styles.sectionTitle}>{title}</Text>}
      <View style={styles.surface}>
        {rows.map((child, i) => (
          <View key={i} style={[styles.row, i > 0 && styles.rowDivided]}>
            {child}
          </View>
        ))}
      </View>
    </View>
  );
}

/** Card is for the few things that really are one standalone object. */
export function Card({
  title,
  children,
}: {
  title?: string;
  children: React.ReactNode;
}): React.JSX.Element {
  return (
    <View style={styles.card}>
      {title !== undefined && <Text style={styles.heading}>{title}</Text>}
      {children}
    </View>
  );
}

/** Notice reports an outcome. It shows what happened, never clipboard content. */
export function Notice({
  message,
  tone = 'info',
}: {
  message: string;
  tone?: 'info' | 'ok' | 'error';
}): React.JSX.Element | null {
  if (message === '') {
    return null;
  }
  const palette =
    tone === 'error'
      ? { bg: colors.dangerSoft, fg: colors.danger, icon: 'alert' as const }
      : tone === 'ok'
        ? { bg: colors.okSoft, fg: colors.ok, icon: 'check' as const }
        : { bg: colors.accentSoft, fg: colors.text, icon: 'check' as const };
  return (
    <View style={[styles.notice, { backgroundColor: palette.bg }]}>
      <Icon name={palette.icon} size={16} color={palette.fg} />
      <Text style={[styles.noticeText, { color: palette.fg }]}>{message}</Text>
    </View>
  );
}

/** Status is the one line a screen reports through, kept for the places that
 *  want text with no block around it. */
export function Status({
  message,
  ok,
}: {
  message: string;
  ok?: boolean;
}): React.JSX.Element | null {
  if (message === '') {
    return null;
  }
  return (
    <Text style={[styles.muted, { color: ok === false ? colors.danger : colors.muted }]}>
      {message}
    </Text>
  );
}

/** Sheet is the app's modal: a scrim over the screen and a panel centred on it. */
export function Sheet({
  visible,
  title,
  onClose,
  children,
  dismissable = true,
}: {
  visible: boolean;
  title: string;
  onClose: () => void;
  children: React.ReactNode;
  dismissable?: boolean;
}): React.JSX.Element {
  return (
    <Modal
      visible={visible}
      transparent
      animationType="fade"
      statusBarTranslucent
      navigationBarTranslucent
      onRequestClose={onClose}
    >
      <Pressable
        style={styles.scrim}
        accessibilityRole="button"
        accessibilityLabel={dismissable ? 'Dismiss' : title}
        onPress={dismissable ? onClose : undefined}
      >
        {/* The inner Pressable swallows taps so the panel itself does not
            dismiss the sheet it is sitting on. */}
        <Pressable style={styles.sheet} onPress={() => undefined}>
          <View style={styles.rowInline}>
            <Text style={[styles.sheetTitle, { flex: 1 }]}>{title}</Text>
            {dismissable && (
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Close"
                onPress={onClose}
                hitSlop={12}
                style={{
                  width: touchTarget - 12,
                  height: touchTarget - 12,
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <Icon name="close" size={20} color={colors.muted} />
              </Pressable>
            )}
          </View>
          {children}
        </Pressable>
      </Pressable>
    </Modal>
  );
}

/** Choice is one option in the direction dialog. */
export function Choice({
  icon,
  title,
  why,
  onPress,
  disabled,
}: {
  icon: IconName;
  title: string;
  why: string;
  onPress: () => void;
  disabled?: boolean;
}): React.JSX.Element {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`${title}. ${why}`}
      accessibilityState={{ disabled: disabled === true }}
      onPress={onPress}
      disabled={disabled}
      android_ripple={{ color: colors.accentSoft }}
      style={[styles.choice, disabled === true && styles.buttonDisabled]}
    >
      <Icon name={icon} size={22} color={colors.accent} />
      <View style={styles.choiceBody}>
        <Text style={styles.choiceTitle}>{title}</Text>
        <Text style={styles.choiceWhy}>{why}</Text>
      </View>
    </Pressable>
  );
}

/**
 * Screen is the scrolling body every screen with an input is built on.
 *
 * Android's `adjustResize` used to be all this needed: the window shrank and a
 * ScrollView did the rest. Under the edge-to-edge this app is required to use
 * (SDK 35+) the window no longer resizes, so the keyboard simply covers the
 * bottom of the screen — which is where the Join and Create fields are. The
 * KeyboardAvoidingView is what puts that back, and it is here rather than in
 * each screen so no screen can forget it.
 *
 * `keyboardShouldPersistTaps="handled"` is the other half: without it the first
 * tap on a button while the keyboard is up is swallowed by the dismiss, and the
 * user has to press twice.
 */
export function Screen({
  children,
  extraBottom = 0,
}: {
  children: React.ReactNode;
  /** extraBottom is room for anything drawn over the scroll, e.g. the tab bar. */
  extraBottom?: number;
}): React.JSX.Element {
  // behavior="padding" on both platforms: with no window resize left to rely
  // on, growing the content's own bottom inset is what actually moves it clear
  // of the keyboard.
  return (
    <KeyboardAvoidingView style={styles.screen} behavior="padding">
      <ScrollView
        style={styles.screen}
        contentContainerStyle={[styles.content, { paddingBottom: 32 + extraBottom }]}
        keyboardShouldPersistTaps="handled"
        keyboardDismissMode="interactive"
      >
        {children}
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

/**
 * CopyButton puts a string on the clipboard and says it did.
 *
 * What it copies is this app's own UI text — a pairing code being handed to
 * another device — so it goes through the port's `writeText` rather than the
 * entry path: nothing here is a clipboard entry and nothing travels to the
 * group.
 */
export function CopyButton({
  value,
  label = 'Copy',
  onResult,
}: {
  value: string;
  label?: string;
  onResult?: (message: string, ok: boolean) => void;
}): React.JSX.Element {
  const [done, setDone] = React.useState(false);

  React.useEffect(() => {
    if (!done) {
      return;
    }
    const id = setTimeout(() => setDone(false), 1800);
    return () => clearTimeout(id);
  }, [done]);

  return (
    <Button
      label={done ? 'Copied' : label}
      icon={done ? 'check' : undefined}
      variant="secondary"
      onPress={() => {
        void clipboard
          .writeText(value)
          .then(() => {
            setDone(true);
            onResult?.('Copied. Paste it on the other device.', true);
          })
          .catch((err: unknown) => onResult?.(failureMessage(err), false));
      }}
    />
  );
}
