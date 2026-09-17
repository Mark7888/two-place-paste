/** The three controls every screen is built from. */

import React from 'react';
import { Pressable, Text, View } from 'react-native';

import { colors, styles } from './theme';

export function Button({
  label,
  onPress,
  disabled,
  variant = 'primary',
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  variant?: 'primary' | 'secondary' | 'danger';
}): React.JSX.Element {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: disabled === true }}
      onPress={onPress}
      disabled={disabled}
      style={[
        styles.button,
        variant === 'secondary' && styles.buttonSecondary,
        variant === 'danger' && styles.buttonDanger,
        disabled === true && styles.buttonDisabled,
      ]}
    >
      <Text style={[styles.buttonLabel, variant === 'secondary' && styles.buttonLabelSecondary]}>
        {label}
      </Text>
    </Pressable>
  );
}

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

/** Status is the one line every screen reports through. It shows outcomes, never clipboard content. */
export function Status({ message, ok }: { message: string; ok?: boolean }): React.JSX.Element | null {
  if (message === '') {
    return null;
  }
  return (
    <Text style={[styles.muted, { color: ok === false ? colors.danger : colors.muted }]}>
      {message}
    </Text>
  );
}
