/**
 * How an opened history entry or store item is drawn, from what
 * src/app/preview.ts made of it.
 */

import React, { useState } from 'react';
import { Image, Text, View } from 'react-native';

import { formatSize, type Preview } from './preview';
import { styles } from './theme';

/** PreviewBody renders what a preview of a history entry or a store item
 *  turned out to be. */
export function PreviewBody({ preview }: { preview: Preview }): React.JSX.Element {
  if (preview.kind === 'image') {
    if (preview.imageTooLarge || preview.imageUri === '') {
      return (
        <Text style={styles.small}>
          {preview.format} · {formatSize(preview.bytes)} — too large to show here.
        </Text>
      );
    }
    return (
      <>
        <ImagePreview uri={preview.imageUri} />
        <Text style={styles.small}>
          {preview.format} · {formatSize(preview.bytes)}
        </Text>
      </>
    );
  }

  if (preview.kind === 'file') {
    if (preview.mismatch) {
      return (
        <Text style={styles.small}>
          This file’s content does not match its type, so it is not shown here. Save or open it instead.
        </Text>
      );
    }
    // A file has no body worth drawing; its name and size are the preview.
    return (
      <Text style={styles.small}>
        {preview.filename === '' ? 'file' : preview.filename} · {preview.contentType} ·{' '}
        {formatSize(preview.bytes)}
      </Text>
    );
  }

  return (
    <>
      <View style={styles.previewBox}>
        <Text style={styles.previewText}>
          {preview.text === '' ? '(nothing to show)' : preview.text}
        </Text>
      </View>
      <Text style={styles.small}>
        {preview.truncated ? 'Shown in part · ' : ''}
        {preview.format} · {formatSize(preview.bytes)}
      </Text>
    </>
  );
}

/**
 * ImagePreview draws the image at its own aspect ratio.
 *
 * The ratio comes from the decoded image itself, through `onLoad`, because
 * nothing in an entry's metadata carries it. Until it arrives the box has a
 * provisional height; `resizeMode="contain"` means the picture is never
 * distorted at any point, only letterboxed.
 *
 * A landscape image takes the full width and derives its height; a portrait one
 * is pinned to a maximum height and derives its width, so a tall screenshot
 * does not take over the whole screen.
 */
function ImagePreview({ uri }: { uri: string }): React.JSX.Element {
  const [ratio, setRatio] = useState<number | null>(null);

  const box =
    ratio === null
      ? { width: '100%' as const, height: 160 }
      : ratio >= 1
        ? { width: '100%' as const, aspectRatio: ratio }
        : { height: 280, aspectRatio: ratio, alignSelf: 'flex-start' as const };

  return (
    <Image
      accessibilityIgnoresInvertColors
      source={{ uri }}
      resizeMode="contain"
      onLoad={(e) => {
        const { width, height } = e.nativeEvent.source;
        if (width > 0 && height > 0) {
          setRatio(width / height);
        }
      }}
      style={[styles.previewImage, box]}
    />
  );
}
