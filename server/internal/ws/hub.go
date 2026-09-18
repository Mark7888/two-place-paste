package ws

import (
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// Hub tracks live connections so the server can fan out to a group, reach one
// device, or close a revoked device's socket (SPEC §3.2 step 3, §3.3 step 5).
//
// It holds no state that matters: everything durable is in the store. Losing
// the hub costs the clients a reconnect and nothing else.
type Hub struct {
	mu sync.RWMutex

	// groups maps a group id to the connections currently authenticated to it.
	groups map[string]map[*conn]struct{}

	// devices maps a device id to its connection. A device has at most one:
	// a second connection replaces the first, so a client that reconnects
	// without a clean close does not leave a phantom behind.
	devices map[string]*conn

	// offers maps a pairing offer code to the unauthenticated connection
	// waiting on it. A device that minted an offer has no identity yet — that
	// is the whole point of the flow — so this is the only way to reach it
	// with the PairingComplete a member's accept produces.
	offers map[string]*conn
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{
		groups:  make(map[string]map[*conn]struct{}),
		devices: make(map[string]*conn),
		offers:  make(map[string]*conn),
	}
}

// register adds an authenticated connection, displacing any earlier
// connection of the same device.
func (h *Hub) register(c *conn) {
	deviceID, groupID := c.identity()
	if deviceID == "" || groupID == "" {
		return
	}

	h.mu.Lock()
	displaced := h.devices[deviceID]
	h.devices[deviceID] = c
	if h.groups[groupID] == nil {
		h.groups[groupID] = make(map[*conn]struct{})
	}
	h.groups[groupID][c] = struct{}{}
	if displaced != nil {
		delete(h.groups[groupID], displaced)
	}
	h.mu.Unlock()

	if displaced != nil && displaced != c {
		displaced.closeWith(websocket.StatusNormalClosure, "replaced by a newer connection")
	}
}

// unregister removes a connection, including any pairing offers it was waiting
// on: an offer whose joiner has gone away can no longer be completed, and a
// member that accepts it is told so rather than admitting a device that will
// never learn its key.
func (h *Hub) unregister(c *conn) {
	deviceID, groupID := c.identity()
	codes := c.offerCodes()

	h.mu.Lock()
	defer h.mu.Unlock()

	if peers := h.groups[groupID]; peers != nil {
		delete(peers, c)
		if len(peers) == 0 {
			delete(h.groups, groupID)
		}
	}
	if h.devices[deviceID] == c {
		delete(h.devices, deviceID)
	}
	for _, code := range codes {
		if h.offers[code] == c {
			delete(h.offers, code)
		}
	}
}

// waitForOffer records that c is holding its socket open for one offer.
func (h *Hub) waitForOffer(code string, c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.offers[code] = c
}

// sendToOffer queues a frame for the connection waiting on an offer and
// reports whether it was queued. Unlike sendTo, a false result is fatal to the
// flow: the offering device holds no group key and no device identity, so
// there is nothing for it to pick up later.
func (h *Hub) sendToOffer(code string, frame []byte) bool {
	h.mu.Lock()
	c := h.offers[code]
	delete(h.offers, code)
	h.mu.Unlock()

	if c == nil {
		return false
	}
	return c.send(frame)
}

// forgetOffer drops an offer's route without sending anything.
func (h *Hub) forgetOffer(code string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.offers, code)
}

// sendTo queues a frame for one device and reports whether it was queued. A
// false result is not an error: a device that is offline picks up whatever it
// missed on its next connect (SPEC §3.3).
func (h *Hub) sendTo(deviceID string, frame []byte) bool {
	h.mu.RLock()
	c := h.devices[deviceID]
	h.mu.RUnlock()

	if c == nil {
		return false
	}
	return c.send(frame)
}

// broadcast queues a frame for every connection in a group except the devices
// named in except.
func (h *Hub) broadcast(groupID string, frame []byte, except ...string) {
	skip := make(map[string]struct{}, len(except))
	for _, id := range except {
		skip[id] = struct{}{}
	}

	h.mu.RLock()
	peers := make([]*conn, 0, len(h.groups[groupID]))
	for c := range h.groups[groupID] {
		deviceID, _ := c.identity()
		if _, skipped := skip[deviceID]; skipped {
			continue
		}
		peers = append(peers, c)
	}
	h.mu.RUnlock()

	for _, c := range peers {
		c.send(frame)
	}
}

// closeDevice terminates a device's connection. It is called immediately after
// the store has deleted the device, so the credential and the socket die
// together (SPEC §3.3 step 5).
func (h *Hub) closeDevice(deviceID string, logger *slog.Logger) {
	h.mu.RLock()
	c := h.devices[deviceID]
	h.mu.RUnlock()

	if c == nil {
		return
	}
	logger.Info("closing revoked device connection", slog.String("device_id", deviceID))
	c.closeWith(websocket.StatusPolicyViolation, "device revoked")
}
