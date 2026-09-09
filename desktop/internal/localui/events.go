package localui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// eventWriteTimeout bounds one push. A browser tab that stopped reading must
// not hold a goroutine and a subscription open forever.
const eventWriteTimeout = 10 * time.Second

// handleEvents streams service events to the UI over a WebSocket.
//
// The upgrade has already passed the same token and Origin checks as every
// other route (see routes); OriginPatterns below is the library's own check on
// top of that, because an upgrade a page can make is an upgrade a page can
// abuse, and this is the one request where forgetting the check is the
// classic mistake.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.patterns,
	})
	if err != nil {
		s.logger.WarnContext(r.Context(), "the event stream could not be opened", "error", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx := conn.CloseRead(r.Context())
	events, stop := s.api.Subscribe()
	defer stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				_ = conn.Close(websocket.StatusNormalClosure, "service stopped")
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, eventWriteTimeout)
			err := wsjson.Write(writeCtx, conn, ev)
			cancel()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.logger.DebugContext(ctx, "the event stream ended", "error", err)
				}
				return
			}
		}
	}
}
