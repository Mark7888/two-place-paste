/** The controls every screen is built from. */

import React from 'react';
import Svg, { Defs, LinearGradient, Rect, Stop } from 'react-native-svg';
import {
  Animated,
  Easing,
  KeyboardAvoidingView,
  Modal,
  Pressable,
  ScrollView,
  Text,
  View,
} from 'react-native';

import type { Progress } from '../core';
import { clipboard, files, type SavedFile } from '../platform';

import { Icon, type IconName } from './icons';
import { transferText } from './preview';
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
  action,
}: {
  message: string;
  tone?: 'info' | 'ok' | 'error';
  /** action is one button at the right of the notice, such as "Open" after a save. */
  action?: { label: string; onPress: () => void; disabled?: boolean };
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
      {action !== undefined && (
        <Pressable
          accessibilityRole="button"
          accessibilityLabel={action.label}
          accessibilityState={{ disabled: action.disabled === true }}
          onPress={action.onPress}
          disabled={action.disabled}
          hitSlop={8}
          android_ripple={{ color: 'rgba(255,255,255,0.12)', borderless: true }}
          style={[
            {
              borderWidth: 1,
              borderColor: palette.fg,
              borderRadius: 8,
              paddingHorizontal: 12,
              paddingVertical: 6,
              alignSelf: 'center',
            },
            action.disabled === true && styles.buttonDisabled,
          ]}
        >
          <Text style={{ color: palette.fg, fontWeight: '600', fontSize: 14 }}>{action.label}</Text>
        </Pressable>
      )}
    </View>
  );
}

/** TransferBar shows a large entry moving in chunks: which way, and how far. */
export function TransferBar({ transfer }: { transfer: Progress | null }): React.JSX.Element | null {
  if (transfer === null || transfer.total <= 0) {
    return null;
  }
  const { title, detail, percent } = transferText(transfer);
  // The text is styled here rather than with `noticeText`: that style has
  // `flex: 1`, which in this column layout gives the text a height of zero —
  // which is how the bar came to be drawn with no words on it.
  return (
    <View
      style={[styles.notice, { backgroundColor: colors.accentSoft, flexDirection: 'column', alignItems: 'stretch', gap: 2 }]}
      accessibilityRole="progressbar"
      accessibilityLabel={`${title}, ${detail}`}
      accessibilityValue={{ min: 0, max: 100, now: percent }}
    >
      <Text style={{ color: colors.text, fontSize: 14, fontWeight: '600' }} numberOfLines={1} ellipsizeMode="middle">
        {title}
      </Text>
      <Text style={{ color: colors.muted, fontSize: 13, lineHeight: 19 }}>{detail}</Text>
      <View style={{ height: 6, borderRadius: 3, backgroundColor: colors.border, marginTop: 6, overflow: 'hidden' }}>
        <View style={{ height: 6, width: `${percent}%`, backgroundColor: colors.accent }} />
      </View>
    </View>
  );
}

/**
 * TransferList shows every chunked transfer in flight, one bar each: the one
 * running and any queued behind it, so a second download does not make the
 * first one's bar jump or vanish.
 */
export function TransferList({
  transfers,
}: {
  transfers: readonly Progress[];
}): React.JSX.Element | null {
  if (transfers.length === 0) {
    return null;
  }
  return (
    <View style={{ gap: 8 }}>
      {transfers.map((t) => (
        <TransferBar key={`${t.direction}:${t.entryId}`} transfer={t} />
      ))}
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

/**
 * FileChoices is the question put when a received file cannot go on the
 * clipboard: keep it in Downloads, or look at it now without keeping it.
 */
export function FileChoices({
  name,
  onChoose,
  disabled,
}: {
  name: string;
  onChoose: (choice: 'save' | 'open') => void;
  disabled?: boolean;
}): React.JSX.Element {
  return (
    <>
      <Text style={styles.muted}>
        {name} is a file, and Android’s clipboard cannot hold files.
      </Text>
      <Choice
        icon="download"
        title="Save to Downloads"
        why="Keep a copy in Downloads/TwoPlacePaste on this device."
        disabled={disabled}
        onPress={() => onChoose('save')}
      />
      <Choice
        icon="open"
        title="Open"
        why="Show it in an app that opens this type. Nothing is saved to Downloads."
        disabled={disabled}
        onPress={() => onChoose('open')}
      />
    </>
  );
}

/**
 * OutcomeNotice is a screen's last outcome. After a file was saved to
 * Downloads it carries an Open button that opens that saved file.
 */
export function OutcomeNotice({
  message,
  ok,
  saved,
  onError,
}: {
  message: string;
  ok: boolean;
  saved: SavedFile | null;
  onError: (message: string) => void;
}): React.JSX.Element | null {
  const [opening, setOpening] = React.useState(false);
  const open = () => {
    if (saved === null || opening) {
      return;
    }
    setOpening(true);
    files
      .openSaved(saved)
      .catch((err: unknown) => onError(failureMessage(err)))
      .finally(() => setOpening(false));
  };
  return (
    <Notice
      message={message}
      tone={ok ? 'ok' : 'error'}
      action={ok && saved !== null ? { label: 'Open', onPress: open, disabled: opening } : undefined}
    />
  );
}

/**
 * Shimmer is a thin light that sweeps left to right along the top edge of a
 * card, for as long as `active` is true: the History row whose entry is being
 * downloaded right now. It sits over the card's top edge, so the card needs
 * `overflow: 'hidden'` for the band to follow its rounded corners.
 */
export function Shimmer({ active }: { active: boolean }): React.JSX.Element | null {
  const [width, setWidth] = React.useState(0);
  const x = React.useRef(new Animated.Value(0)).current;

  React.useEffect(() => {
    if (!active || width === 0) {
      return;
    }
    x.setValue(0);
    const loop = Animated.loop(
      Animated.timing(x, {
        toValue: 1,
        duration: 1300,
        easing: Easing.inOut((t) => Easing.quad(t)),
        useNativeDriver: true,
      }),
    );
    loop.start();
    return () => loop.stop();
  }, [active, width, x]);

  if (!active) {
    return null;
  }
  const band = Math.max(80, width * 0.45);
  const translateX = x.interpolate({ inputRange: [0, 1], outputRange: [-band, width] });
  return (
    <View
      pointerEvents="none"
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      onLayout={(e) => setWidth(e.nativeEvent.layout.width)}
      style={{ position: 'absolute', top: 0, left: 0, right: 0, height: 3, overflow: 'hidden' }}
    >
      <Animated.View style={{ width: band, height: 3, transform: [{ translateX }] }}>
        <Svg width={band} height={3}>
          <Defs>
            <LinearGradient id="shimmer" x1="0" y1="0" x2="1" y2="0">
              <Stop offset="0" stopColor={colors.accent} stopOpacity={0} />
              <Stop offset="0.5" stopColor={colors.accent} stopOpacity={1} />
              <Stop offset="1" stopColor={colors.accent} stopOpacity={0} />
            </LinearGradient>
          </Defs>
          <Rect x={0} y={0} width={band} height={3} fill="url(#shimmer)" />
        </Svg>
      </Animated.View>
    </View>
  );
}
