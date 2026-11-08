/**
 * The React Native internals the app relies on and React Native does not
 * promise, checked against the React Native this app is built with.
 *
 * A large shared upload is sent a chunk at a time straight from a staged file
 * (`src/platform/socket.android.ts`, `ShareModule.sendSpliced`). That needs
 * two things React Native does not document: the id its JavaScript WebSocket
 * keeps as `_socketId` must be the id its native module files the socket
 * under, and the native module must have a method that sends okio bytes.
 *
 * Neither can be seen from a running test — there is no React Native under
 * Jest — so this reads React Native's own sources. If an upgrade renames or
 * reshapes either, this fails, before the app quietly falls back to reading
 * every chunk into JavaScript (slower, but still correct). The Android build
 * catches a missing `sendBinary(ByteString, Int)` too; `_socketId` only this
 * test can catch.
 */

import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const reactNative = join(__dirname, '../../../node_modules/react-native');
const read = (path: string) => readFileSync(join(reactNative, path), 'utf8');

describe('the React Native WebSocket internals spliced uploads rely on', () => {
  const js = read('Libraries/WebSocket/WebSocket.js');
  const kotlin = read('ReactAndroid/src/main/java/com/facebook/react/modules/websocket/WebSocketModule.kt');

  test('the JavaScript WebSocket keeps its native id as a number in _socketId', () => {
    expect(js).toMatch(/^\s*_socketId:\s*number;/m);
    expect(js).toMatch(/this\._socketId\s*=\s*nextWebSocketId\+\+;/);
  });

  test('that id is the one the socket is opened and sent on natively', () => {
    expect(js).toMatch(/NativeWebSocketModule\.connect\([^;]*,\s*this\._socketId\);/);
    expect(js).toMatch(/NativeWebSocketModule\.sendBinary\([^;]*,\s*this\._socketId\);/);
  });

  test('the native module files the socket under that id', () => {
    expect(kotlin).toMatch(/override fun connect\([\s\S]*?socketID: Double,[\s\S]*?val id = socketID\.toInt\(\)/);
    expect(kotlin).toMatch(/webSocketConnections\[id\] = webSocket/);
  });

  test('the native module can send okio bytes on a socket by its id', () => {
    expect(kotlin).toMatch(/public fun sendBinary\(byteString: ByteString, id: Int\)/);
  });
});
