package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
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

// handleHello states the protocol levels and capabilities this relay
// supports (SPEC §5.3). Nothing in the answer is specific to a group or a
// device, which is why it is legal on an unauthenticated connection.
func (s *Server) handleHello(env *tppv1.Envelope) ([]byte, error) {
	var req tppv1.HelloRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_HELLO_RESPONSE, &tppv1.HelloResponse{
		ProtocolMin:  s.protocolMin.Load(),
		ProtocolMax:  ProtocolMax,
		Capabilities: capabilities,
	})
}

// capabilities are the features this relay offers beyond its protocol level.
// None exist yet; a feature an app has to check for adds its Capability here.
var capabilities []tppv1.Capability

// outdatedBody is the body of a refusal for age. An app recognises the
// refusal by it, not by the status: the WebSocket library answers 426 to any
// request that is not an upgrade, so a plain GET from an app the relay
// accepts gets a 426 too.
type outdatedBody struct {
	Error       string `json:"error"`
	ProtocolMin uint32 `json:"protocol_min"`
	ProtocolMax uint32 `json:"protocol_max"`
}

// refuseOutdated answers an app below the relay's minimum level (SPEC §5.3).
// A plain GET of the endpoint gets the same answer as an upgrade, because a
// React Native WebSocket cannot read the status of a refused upgrade and asks
// again that way.
func (s *Server) refuseOutdated(w http.ResponseWriter) {
	body, err := json.Marshal(outdatedBody{
		Error:       "client_outdated",
		ProtocolMin: s.protocolMin.Load(),
		ProtocolMax: ProtocolMax,
	})
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUpgradeRequired)
	_, _ = w.Write(body)
}
