/**
 * The relay socket on Android: the platform's WebSocket, plus `sendSpliced`
 * and `capture`.
 *
 * React Native's WebSocket takes binary from JavaScript only as base64, which
 * the engine here encodes slowly — for a large upload, slower than the
 * network. Its native module sends bytes directly, so a chunk of a staged,
 * sealed file is sent from there instead: JavaScript writes the frame's head,
 * and `ShareModule.sendSpliced` appends the slice and sends it on this same
 * socket (`Connection.callSpliced`).
 *
 * Downloads come in the same way in reverse: React Native hands JavaScript
 * every binary frame as base64, so a range reply JavaScript arms a capture
 * for has its data written to a staged file by `CaptureHandler.kt`, a content
 * handler on this socket, and arrives without it (`Connection.callCapturing`).
 *
 * It needs the socket's id in the native module, which React Native keeps on
 * the WebSocket as `_socketId` and does not document. Where that is missing —
 * another React Native, a test — the socket is the plain one, and uploads
 * read their chunks into JavaScript as before.
 */

import { encodeBase64Std } from '../core/bytes';
import { wrapWebSocket, type SocketFactory } from '../core/protocol/connection';
import { TppShare } from './native';

export const socketFactory: SocketFactory = (url) => {
  const ws = new WebSocket(url);
  const socket = wrapWebSocket(ws);
  const id = (ws as unknown as { _socketId?: unknown })._socketId;
  const native = TppShare;
  if (typeof id !== 'number' || native === null) {
    return socket;
  }
  return {
    ...socket,
    sendSpliced: (head, slice) => native.sendSpliced(id, encodeBase64Std(head), slice.token, slice.offset, slice.length),
    capture: (envelopeId, responseType, target) =>
      native.armCapture(id, envelopeId, responseType, target.token, target.offset),
    releaseCapture: (envelopeId) => native.releaseCapture(id, envelopeId),
  };
};
