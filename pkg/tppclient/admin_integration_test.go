package tppclient_test

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestGroupNameAndDeletion follows a group through the admin's hands: the
// devices learn its name, hear a rename, and are told — not merely refused —
// when the group is deleted (docs/internal/plans/admin-group-management.md).
func TestGroupNameAndDeletion(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	names := make(chan string, 8)
	var deleted, revoked atomic.Bool
	desktop, store := newClient(t, "desktop", tppclient.Options{
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
		Handlers: tppclient.Handlers{
			OnGroupName:    func(n string) { names <- n },
			OnGroupDeleted: func() { deleted.Store(true) },
			OnRevoked:      func() { revoked.Store(true) },
		},
	})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Anna")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := desktop.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	wantName := func(want string) {
		t.Helper()
		select {
		case got := <-names:
			if got != want {
				t.Errorf("group name = %q, want %q", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the client never learned the group name %q", want)
		}
	}
	// The group starts with its creation token's name.
	wantName("Anna")

	admin := relay.admin(t)
	handle := admin.groupHandle(t, "Anna")
	admin.call(t, http.MethodPut, "/admin/api/groups/"+handle+"/name", map[string]string{"name": "Family"}, http.StatusNoContent, nil)
	wantName("Family")
	if got := desktop.GroupName(); got != "Family" {
		t.Errorf("GroupName() = %q, want Family", got)
	}

	// The name is kept with the rest of the state, for a shell to show before
	// it connects.
	saved, err := tppclient.LoadState(store, "")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.GroupName != "Family" {
		t.Errorf("stored group name = %q, want Family", saved.GroupName)
	}

	admin.call(t, http.MethodDelete, "/admin/api/groups/"+handle, nil, http.StatusOK, nil)
	waitFor(t, "the client to hear its group was deleted", deleted.Load)
	if revoked.Load() {
		t.Error("OnRevoked fired as well as OnGroupDeleted")
	}
	_, err = desktop.GetLatest(ctx)
	if !errors.Is(err, tppclient.ErrGroupDeleted) || !errors.Is(err, tppclient.ErrRevoked) {
		t.Errorf("a request after deletion = %v, want ErrGroupDeleted (which is ErrRevoked)", err)
	}

	// A copy of the same install that was offline at the time learns it on
	// connect, from the relay's 410.
	var laterDeleted atomic.Bool
	offline, err := tppclient.New(tppclient.Options{
		Keystore: store,
		Handlers: tppclient.Handlers{OnGroupDeleted: func() { laterDeleted.Store(true) }},
	})
	if err != nil {
		t.Fatalf("New from stored state: %v", err)
	}
	t.Cleanup(func() { _ = offline.Close() })
	if err := offline.Connect(ctx); !errors.Is(err, tppclient.ErrGroupDeleted) {
		t.Errorf("Connect after deletion = %v, want ErrGroupDeleted", err)
	}
	waitFor(t, "the offline copy to hear its group was deleted", laterDeleted.Load)
}
