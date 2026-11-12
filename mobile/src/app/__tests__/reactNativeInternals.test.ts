/**
 * The React Native internals the app relies on and React Native does not
 * promise, checked against the React Native this app is built with.
 *
 * A large upload is sent a chunk at a time straight from a staged file
 * (`src/platform/socket.android.ts`, `ShareModule.sendSpliced`), and a large
 * download's ranges are captured straight into one (`CaptureHandler.kt`).
 * That needs things React Native does not document: the id its JavaScript
 * WebSocket keeps as `_socketId` must be the id its native module files the
 * socket under; the native module must send okio bytes; and it must let a
 * content handler see each frame and fill in what JavaScript is handed,
 * which JavaScript must decode from base64 as it always has.
 *
 * None of it can be seen from a running test — there is no React Native
 * under Jest — so this reads React Native's own sources. If an upgrade
 * renames or reshapes any of it, this fails, before the app quietly falls
 * back to moving every chunk through JavaScript (slower, but still correct)
 * or, worse, hands JavaScript frames it cannot read. The Android build
 * catches a missing method too; the rest only this test can catch.
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

  test('a content handler sees every binary frame and fills in what JavaScript gets', () => {
    expect(kotlin).toMatch(/public interface ContentHandler \{[\s\S]*?public fun onMessage\(text: String, params: WritableMap\)[\s\S]*?public fun onMessage\(byteString: ByteString, params: WritableMap\)/);
    expect(kotlin).toMatch(/public fun setContentHandler\(id: Int, contentHandler: ContentHandler\?\)/);
    // The frame's type is set before the handler runs, and the handler, when
    // there is one, is what fills in the data: CaptureHandler puts base64 there.
    expect(kotlin).toMatch(
      /override fun onMessage\(webSocket: WebSocket, bytes: ByteString\) \{[\s\S]*?params\.putString\("type", "binary"\)[\s\S]*?contentHandler\.onMessage\(bytes, params\)[\s\S]*?sendEvent\("websocketMessage", params\)/,
    );
  });

  test('JavaScript decodes a binary frame from base64, and leaves the handler alone for arraybuffer', () => {
    expect(js).toMatch(/case 'binary':\s*data = base64\.toByteArray\(ev\.data\)\.buffer;/);
    // Only a change to or from 'blob' touches the socket's content handler.
    expect(js).toMatch(/if \(this\._binaryType === 'blob' \|\| binaryType === 'blob'\) \{/);
  });
});
