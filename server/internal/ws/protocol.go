package ws

import (
	"errors"
	"net/http"
	"strconv"
)

// The protocol levels this relay speaks (SPEC §5.3). A level is a single
// integer per build, independent of the relay's version, and it only rises for
// a change that cannot stay compatible. Compatible features are announced as
// capabilities in HelloResponse instead.
//
// ProtocolMin has no setting: a level is retired for a reason, usually
// security, and an operator must not be able to bring it back.
const (
	ProtocolMin uint32 = 1
	ProtocolMax uint32 = 1
)

// protocolParam is the query parameter of the WebSocket URL an app states its
// level in. It is on the URL rather than in a frame so the relay can act on it
// before the upgrade.
const protocolParam = "proto"

// errBadProtocolLevel is a proto parameter that is not a positive integer.
var errBadProtocolLevel = errors.New("the proto parameter must be a positive integer")

// protocolLevel reads the level an app states. An app that predates levels
// sends none, and it speaks level 1 by definition.
func protocolLevel(r *http.Request) (uint32, error) {
	raw := r.URL.Query().Get(protocolParam)
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || n == 0 {
		return 0, errBadProtocolLevel
	}
	return uint32(n), nil
}
