/** The relay socket on iOS: the platform's WebSocket, with nothing added. */

import { webSocketFactory, type SocketFactory } from '../core/protocol/connection';

export const socketFactory: SocketFactory = webSocketFactory;
