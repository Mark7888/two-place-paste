package ws

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

// encode serializes msg into an Envelope of the given type. id is the
// correlation id: a response echoes the request's id verbatim, and a
// server-pushed event carries a fresh one that correlates with nothing
// (SPEC §5.1).
func encode(id string, typ tppv1.MessageType, msg proto.Message) ([]byte, error) {
	var payload []byte
	if msg != nil {
		var err error
		payload, err = proto.Marshal(msg)
		if err != nil {
			return nil, fmt.Errorf("marshal %s payload: %w", typ, err)
		}
	}
	frame, err := proto.Marshal(&tppv1.Envelope{Id: id, Type: typ, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	return frame, nil
}

// decode parses an envelope's payload into msg.
func decode(env *tppv1.Envelope, msg proto.Message) error {
	if err := proto.Unmarshal(env.GetPayload(), msg); err != nil {
		return fmt.Errorf("unmarshal %s payload: %w", env.GetType(), err)
	}
	return nil
}

// errorFrame builds the Error response for a failed request. The message
// reaches the server log and the client's UI, so it names ids, epochs and byte
// lengths only — never ciphertext, a key, a wrapped key or a token
// (SPEC §2.3, docs/conventions.md §1).
func errorFrame(id string, code tppv1.ErrorCode, message string) ([]byte, error) {
	return encode(id, tppv1.MessageType_MESSAGE_TYPE_ERROR, &tppv1.Error{Code: code, Message: message})
}
