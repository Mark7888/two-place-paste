/** The controls every screen is built from. */

import React from 'react';
import { Modal, Pressable, Text, View } from 'react-native';

import { Icon, type IconName } from './icons';
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
