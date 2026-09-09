package ws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// DefaultPath is where the transport is mounted.
const DefaultPath = "/ws"

// Defaults for the tunables below.
const (
	defaultPairingTTL   = 5 * time.Minute // SPEC §3.2
	defaultHistoryLimit = 50
	maxHistoryLimit     = 200

	// frameOverhead is the slack allowed above the ciphertext cap for envelope
	// and protobuf framing. The sum is the socket's read limit, which is what
	// enforces the cap at the reader rather than after buffering (SPEC §4.3).
	frameOverhead = 64 << 10
)

// Options configures a Server. The zero value is usable.
type Options struct {
	// Logger receives connection-scoped logs. Defaults to slog.Default().
	Logger *slog.Logger

	// Path is the WebSocket endpoint. Defaults to DefaultPath.
	Path string

	// PairingTTL is how long a pairing token lives. Defaults to 5 minutes
	// (SPEC §3.2).
	PairingTTL time.Duration

	// DefaultHistoryLimit is the page size for a history request that does not
	// ask for one; MaxHistoryLimit caps what it may ask for.
	DefaultHistoryLimit int
	MaxHistoryLimit     int

	// OriginPatterns are the browser origins allowed to open a socket. Empty
	// means same-origin only, which is what a self-hosted deployment wants:
	// the desktop and mobile clients are not browsers and send no Origin.
	OriginPatterns []string

	// Now supplies UTC timestamps; tests replace it.
	Now func() time.Time
}

// Server is the WebSocket transport and its protocol handlers.
type Server struct {
	store   Store
	entries Entries
	hub     *Hub
	logger  *slog.Logger

	path                string
	pairingTTL          time.Duration
	defaultHistoryLimit int
	maxHistoryLimit     int
	originPatterns      []string
	now                 func() time.Time
}

// New returns a Server over the given store and entry service.
func New(st Store, en Entries, opts Options) *Server {
	s := &Server{
		store:               st,
		entries:             en,
		hub:                 NewHub(),
		logger:              opts.Logger,
		path:                opts.Path,
		pairingTTL:          opts.PairingTTL,
		defaultHistoryLimit: opts.DefaultHistoryLimit,
		maxHistoryLimit:     opts.MaxHistoryLimit,
		originPatterns:      opts.OriginPatterns,
		now:                 opts.Now,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.path == "" {
		s.path = DefaultPath
	}
	if s.pairingTTL <= 0 {
		s.pairingTTL = defaultPairingTTL
	}
	if s.defaultHistoryLimit <= 0 {
		s.defaultHistoryLimit = defaultHistoryLimit
	}
	if s.maxHistoryLimit <= 0 {
		s.maxHistoryLimit = maxHistoryLimit
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	return s
}

// ServeHTTP upgrades the request and serves the connection until it closes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Identity is established before the upgrade, so an unknown device never
	// gets a socket at all.
	var device store.Device
	if deviceID := r.URL.Query().Get("device_id"); deviceID != "" {
		var err error
		device, err = s.store.GetDevice(ctx, deviceID)
		if errors.Is(err, store.ErrNotFound) {
			// Also the answer for a device that was revoked while it was
			// away: its credential died with its record (SPEC §3.3 step 5).
			http.Error(w, "unknown device", http.StatusUnauthorized)
			return
		}
		if err != nil {
			s.logger.ErrorContext(ctx, "device lookup failed", slog.Any("error", err))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	sock, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.originPatterns,
	})
	if err != nil {
		// Accept has already written a response.
		s.logger.DebugContext(ctx, "websocket upgrade failed", slog.Any("error", err))
		return
	}
	sock.SetReadLimit(s.entries.MaxBytes() + frameOverhead)

	c := newConn(sock, s.logger)
	defer func() {
		s.hub.unregister(c)
		c.closeWith(websocket.StatusNormalClosure, "")
	}()

	if device.ID != "" {
		c.setIdentity(device.ID, device.GroupID)
		s.hub.register(c)
		s.onAuthenticated(ctx, c, device)
	}

	go c.writePump(ctx)
	s.readLoop(ctx, c)
}

// onAuthenticated runs the per-connection work that follows a successful
// identity check.
func (s *Server) onAuthenticated(ctx context.Context, c *conn, device store.Device) {
	if err := s.store.TouchLastSeen(ctx, device.ID); err != nil {
		s.logger.WarnContext(ctx, "recording last seen failed",
			slog.String("device_id", device.ID), slog.Any("error", err))
	}

	// A wrapped key waits for a device rather than requiring it to be online
	// during the rekey, so it is delivered on connect (SPEC §3.3). The client
	// compares epochs and ignores one it already has; the server cannot know
	// which epoch the device is running.
	key, err := s.store.GetWrappedKey(ctx, device.ID)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "reading wrapped key failed",
			slog.String("device_id", device.ID), slog.Any("error", err))
		return
	}
	frame, err := encode(newCorrelationID(), tppv1.MessageType_MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE,
		&tppv1.WrappedKeyAvailable{Epoch: key.Epoch, WrappedGroupKey: key.Key})
	if err != nil {
		s.logger.ErrorContext(ctx, "encoding wrapped key event failed", slog.Any("error", err))
		return
	}
	c.send(frame)
}

// readLoop reads frames until the socket closes.
func (s *Server) readLoop(ctx context.Context, c *conn) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			s.logger.DebugContext(ctx, "websocket closed", slog.Any("reason", err))
			return
		}
		if typ != websocket.MessageBinary {
			// Every frame is a serialized Envelope; there is no text protocol
			// to fall back to (SPEC §5.1).
			c.closeWith(websocket.StatusUnsupportedData, "binary frames only")
			return
		}

		var env tppv1.Envelope
		if err := proto.Unmarshal(data, &env); err != nil {
			s.reply(ctx, c, "", nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "envelope could not be decoded"))
			continue
		}

		frame, err := s.dispatch(ctx, c, &env)
		s.reply(ctx, c, env.GetId(), frame, err)
	}
}

// reply sends a handler's response, converting a failure into an Error frame
// that echoes the request id (SPEC §5.1).
func (s *Server) reply(ctx context.Context, c *conn, id string, frame []byte, err error) {
	if err == nil {
		if frame != nil {
			c.send(frame)
		}
		return
	}

	var we wireError
	if !errors.As(err, &we) {
		// Nothing about an internal failure is disclosed beyond the code.
		s.logger.ErrorContext(ctx, "handler failed", slog.String("envelope_id", id), slog.Any("error", err))
		we = wireError{code: tppv1.ErrorCode_ERROR_CODE_INTERNAL, msg: "internal error"}
	}

	errFrame, encodeErr := errorFrame(id, we.code, we.msg)
	if encodeErr != nil {
		s.logger.ErrorContext(ctx, "encoding error frame failed", slog.Any("error", encodeErr))
		return
	}
	c.send(errFrame)
}

// wireError is a failure that maps onto a protocol ErrorCode. Anything else a
// handler returns is an internal error the client learns nothing about.
type wireError struct {
	code tppv1.ErrorCode
	msg  string
}

func (e wireError) Error() string { return fmt.Sprintf("%s: %s", e.code, e.msg) }

func wireErrf(code tppv1.ErrorCode, format string, args ...any) error {
	return wireError{code: code, msg: fmt.Sprintf(format, args...)}
}
