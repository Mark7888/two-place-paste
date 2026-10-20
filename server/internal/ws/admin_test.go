package ws_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/coder/websocket"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/ws"
)

// TestGroupInfoAndRename: a device asks for its group's name, and hears about
// a rename while it is connected.
func TestGroupInfoAndRename(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c, created := h.createGroup(t, "laptop")

	c.send("info-1", tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_REQUEST, &tppv1.GroupInfoRequest{})
	var info tppv1.GroupInfoResponse
	c.await(tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_RESPONSE, &info)
	if info.GetName() != "test" {
		t.Errorf("GroupInfoResponse.Name = %q, want the token's name %q", info.GetName(), "test")
	}

	ctx := context.Background()
	if err := h.store.RenameGroup(ctx, created.GetGroupId(), "Family"); err != nil {
		t.Fatalf("RenameGroup() error = %v", err)
	}
	h.server.AnnounceRename(ctx, created.GetGroupId(), "Family")
	var renamed tppv1.GroupRenamed
	c.await(tppv1.MessageType_MESSAGE_TYPE_GROUP_RENAMED, &renamed)
	if renamed.GetName() != "Family" {
		t.Errorf("GroupRenamed.Name = %q, want %q", renamed.GetName(), "Family")
	}

	c.send("info-2", tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_REQUEST, &tppv1.GroupInfoRequest{})
	c.await(tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_RESPONSE, &info)
	if info.GetName() != "Family" {
		t.Errorf("GroupInfoResponse.Name after rename = %q, want %q", info.GetName(), "Family")
	}

	online := h.server.OnlineDevices(created.GetGroupId())
	if !online[created.GetDeviceId()] || len(online) != 1 {
		t.Errorf("OnlineDevices() = %v, want just %s", online, created.GetDeviceId())
	}
}

// TestDeletedGroupClosesAndRefuses: deleting a group closes its sockets with
// the group-deleted code, and a reconnect gets 410 rather than 401
// (docs/internal/plans/admin-group-management.md D7).
func TestDeletedGroupClosesAndRefuses(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c, created := h.createGroup(t, "laptop")
	other, _ := h.createGroup(t, "elsewhere")

	if _, err := h.store.DeleteGroup(context.Background(), created.GetGroupId()); err != nil {
		t.Fatalf("DeleteGroup() error = %v", err)
	}
	h.server.CloseGroup(created.GetGroupId())

	if got := websocket.CloseStatus(c.awaitClose()); got != ws.GroupDeletedCloseCode {
		t.Errorf("close status = %d, want %d", got, ws.GroupDeletedCloseCode)
	}

	_, resp, err := websocket.Dial(context.Background(), h.url+"?device_id="+created.GetDeviceId(), nil)
	if err == nil {
		t.Fatal("dial as a device of a deleted group succeeded, want it refused")
	}
	if resp == nil || resp.StatusCode != http.StatusGone {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Errorf("status = %d, want %d", code, http.StatusGone)
	}

	// Another group's socket is untouched.
	other.send("info", tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_REQUEST, &tppv1.GroupInfoRequest{})
	other.await(tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_RESPONSE, nil)
}
