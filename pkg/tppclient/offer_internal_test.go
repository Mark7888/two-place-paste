package tppclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

func TestPairingCodeTellsTheTwoKindsApart(t *testing.T) {
	invite := &tppv1.PairingPayload{
		ServerUrl:                 "https://tpp.example.com",
		PairingToken:              "AbCd-1234_x",
		InviterEphemeralPublicKey: bytes.Repeat([]byte{0x2a}, 32),
	}
	offer := &tppv1.PairingOffer{
		ServerUrl:       "https://tpp.example.com",
		OfferCode:       "Zz99-offer_x",
		DevicePublicKey: bytes.Repeat([]byte{0x7f}, 32),
		DeviceName:      "Anna — phone",
	}

	encodedInvite, err := EncodePairingPayload(invite)
	if err != nil {
		t.Fatalf("encode invite: %v", err)
	}
	encodedOffer, err := EncodePairingOffer(offer)
	if err != nil {
		t.Fatalf("encode offer: %v", err)
	}
	if strings.ContainsAny(encodedOffer, "+/=") {
		t.Errorf("offer %q is not unpadded base64url; a QR code and a paste must agree", encodedOffer)
	}
	if encodedInvite == encodedOffer {
		t.Fatal("an invitation and an offer encode to the same string")
	}

	// The whole reason PairingCode exists: protobuf decodes by field number, so
	// without a discriminator an offer would be read as an invitation with a
	// nonsense token rather than rejected.
	for _, tc := range []struct {
		name    string
		code    string
		isOffer bool
	}{
		{"invite", encodedInvite, false},
		{"offer", encodedOffer, true},
		// What a user's clipboard adds.
		{"offer with clipboard noise", " " + encodedOffer + "\n", true},
		{"invite with padding", encodedInvite + "==", false},
	} {
		decoded, err := DecodeCode(tc.code)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if got := decoded.GetOffer() != nil; got != tc.isOffer {
			t.Errorf("%s: decoded as an offer = %v, want %v", tc.name, got, tc.isOffer)
		}
	}

	gotOffer, err := DecodeCode(encodedOffer)
	if err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	o := gotOffer.GetOffer()
	if o.GetOfferCode() != offer.GetOfferCode() || o.GetDeviceName() != offer.GetDeviceName() ||
		!bytes.Equal(o.GetDevicePublicKey(), offer.GetDevicePublicKey()) {
		t.Errorf("round trip changed the offer: %v", o)
	}

	// The two typed entry points refuse the other kind rather than
	// half-understanding it.
	if _, err := DecodePairingPayload(encodedOffer); err == nil {
		t.Error("DecodePairingPayload accepted an offer")
	}
	for _, bad := range []string{"", "not base64!!", "AAAA"} {
		if _, err := DecodeCode(bad); err == nil {
			t.Errorf("DecodeCode(%q) succeeded", bad)
		}
	}
}

// TestInvitesStayReadableByOlderBuilds pins the compatibility decision behind
// EncodePairingPayload: an invitation is still emitted bare, so a build that
// predates PairingCode reads one this build shows, and this build reads one of
// theirs.
func TestInvitesStayReadableByOlderBuilds(t *testing.T) {
	invite := &tppv1.PairingPayload{
		ServerUrl:                 "https://tpp.example.com",
		PairingToken:              "AbCd-1234_x",
		InviterEphemeralPublicKey: bytes.Repeat([]byte{0x2a}, 32),
	}
	encoded, err := EncodePairingPayload(invite)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// An older build decodes the bytes as a bare PairingPayload and nothing
	// else. This is that decoder.
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	var asOldBuildSeesIt tppv1.PairingPayload
	if err := proto.Unmarshal(raw, &asOldBuildSeesIt); err != nil {
		t.Fatalf("an older build could not parse this invitation: %v", err)
	}
	if asOldBuildSeesIt.GetServerUrl() != invite.GetServerUrl() ||
		asOldBuildSeesIt.GetPairingToken() != invite.GetPairingToken() {
		t.Errorf("an older build reads %v, want the invitation unchanged", &asOldBuildSeesIt)
	}

	// And the other direction: what that build emits is what DecodeCode's
	// fallback is for.
	theirs, err := proto.Marshal(invite)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := DecodeCode(base64.RawURLEncoding.EncodeToString(theirs))
	if err != nil {
		t.Fatalf("decode an older build's invitation: %v", err)
	}
	if decoded.GetInvite().GetPairingToken() != invite.GetPairingToken() {
		t.Errorf("decoded %v, want the invitation unchanged", decoded.GetInvite())
	}
}

// TestFingerprintFormat pins the string a user compares across two screens.
// The mobile client has the same test over the same vectors; if these two ever
// disagree the confirmation dialog is worthless, because the user is comparing
// two renderings of the same key that do not look alike.
func TestFingerprintFormat(t *testing.T) {
	for _, tc := range []struct {
		key  []byte
		want string
	}{
		{bytes.Repeat([]byte{0x00}, 32), "6668 7AAD F862 BD77"},
		{bytes.Repeat([]byte{0xff}, 32), "AF96 1376 0F72 635F"},
		{[]byte("tpp"), "1B3C EA64 57B6 9A77"},
	} {
		if got := Fingerprint(tc.key); got != tc.want {
			t.Errorf("Fingerprint(%x) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestOfferRefusesADeviceThatIsAlreadyInAGroup: a device holding a group key
// cannot offer itself elsewhere without discarding the key it has, and
// discarding it is a separate, deliberate act.
func TestOfferRefusesADeviceThatIsAlreadyInAGroup(t *testing.T) {
	c := newOfflineClient(t, 3)
	if _, err := c.StartOffer(context.Background(), "https://tpp.example.com"); err == nil {
		t.Error("StartOffer on a paired device succeeded, want it refused")
	}
}

// TestStartOfferNeedsARelayURL is the one real cost of this direction, stated
// as a test: a device with no group has no relay URL either.
func TestStartOfferNeedsARelayURL(t *testing.T) {
	c, err := New(Options{DeviceName: "unpaired"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.StartOffer(context.Background(), ""); err == nil {
		t.Error("StartOffer with no relay URL succeeded, want it refused")
	}
}

// TestPrepareAcceptOfferIsTheOnlyWayIn asserts the consent gate of
// docs/plans/joiner-emitted-pairing.md §5 in the client rather than in a
// screen: there is no method that admits a device without a prepared
// acceptance, and preparing one contacts nothing.
func TestPrepareAcceptOfferIsTheOnlyWayIn(t *testing.T) {
	c := newOfflineClient(t, 1)
	ctx := context.Background()

	offer, err := EncodePairingOffer(&tppv1.PairingOffer{
		ServerUrl:       c.State().ServerURL,
		OfferCode:       "Zz99-offer_x",
		DevicePublicKey: bytes.Repeat([]byte{0x7f}, 32),
		DeviceName:      "Anna — phone",
	})
	if err != nil {
		t.Fatalf("encode offer: %v", err)
	}

	// This client has no connection at all. PrepareAcceptOffer still returns,
	// because it changes nothing and talks to nobody — which is exactly what
	// makes the dialog it feeds honest.
	prepared, err := c.PrepareAcceptOffer(ctx, offer)
	if err != nil {
		t.Fatalf("PrepareAcceptOffer: %v", err)
	}
	if prepared.DeviceName != "Anna — phone" {
		t.Errorf("DeviceName = %q, want the offered name", prepared.DeviceName)
	}
	if prepared.Fingerprint != Fingerprint(bytes.Repeat([]byte{0x7f}, 32)) {
		t.Errorf("Fingerprint = %q, want the offered key's", prepared.Fingerprint)
	}
	if prepared.Fingerprint == "" || prepared.DeviceName == "" {
		t.Error("a confirmation dialog cannot be rendered from this")
	}

	// An invitation is not an offer: accepting one would mean wrapping a key
	// for a device that is waiting to be invited, not offering itself.
	invite, err := EncodePairingPayload(&tppv1.PairingPayload{
		ServerUrl:    c.State().ServerURL,
		PairingToken: "AbCd-1234_x",
	})
	if err != nil {
		t.Fatalf("encode invite: %v", err)
	}
	if _, err := c.PrepareAcceptOffer(ctx, invite); err == nil {
		t.Error("PrepareAcceptOffer accepted an invitation")
	}

	// An offer held by another relay could not be completed over this client's
	// own connection, and is refused where the user can still read why.
	elsewhere, err := EncodePairingOffer(&tppv1.PairingOffer{
		ServerUrl:       "https://somewhere-else.example.com",
		OfferCode:       "Zz99-offer_x",
		DevicePublicKey: bytes.Repeat([]byte{0x7f}, 32),
		DeviceName:      "Anna — phone",
	})
	if err != nil {
		t.Fatalf("encode offer: %v", err)
	}
	if _, err := c.PrepareAcceptOffer(ctx, elsewhere); err == nil {
		t.Error("PrepareAcceptOffer accepted an offer held by another relay")
	}
}
