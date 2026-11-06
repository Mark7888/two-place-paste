package tppclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
)

// Several groups on one installation (/spec/crypto.md §10.1,
// docs/internal/plans/multi-group-desktop.md).
//
// One group is active: its state is the client's State, kept under the
// keystore entry a single-group build reads, so a downgrade carries on with
// it. The others are parked, together, under one more entry. A parked group
// is not a second connection: nothing of it is loaded, and to its group it is
// a device that is offline. Switching stops the client the way Forget does,
// swaps the state underneath it, and leaves the caller to Connect.

// DefaultParkedName is the keystore entry parked groups are kept under.
const DefaultParkedName = "parked-groups"

// ErrUnknownGroup means a ref names no parked group: it was removed, or
// switched to, by an earlier request.
var ErrUnknownGroup = errors.New("tppclient: no parked group has that ref")

// ErrParkedUnreadable means the stored parked groups could not be read — a
// newer build's format, or a damaged record. They are left exactly as they
// are rather than overwritten, so nothing that changes the parked list runs.
var ErrParkedUnreadable = errors.New("tppclient: the parked groups could not be read")

// parkedVersion is the shape of the parked record this build reads and
// writes.
const parkedVersion = 1

// parkedRecord is what the parked entry holds. Each element is a State, the
// very struct the active entry holds, so a parked group goes back under the
// active entry byte for byte.
type parkedRecord struct {
	Version int     `json:"version"`
	Groups  []State `json:"groups"`
}

// ParkedGroup is a parked group as a UI shows it. It carries no id and no
// key: a UI addresses the group by Ref.
type ParkedGroup struct {
	// Ref names the group to SwitchGroup and RemoveParked (GroupRef).
	Ref string

	// GroupName is the group's display name as it was when it was parked.
	GroupName string

	// ServerURL is the relay the group lives on.
	ServerURL string
}

// groupRefInfo separates a ref's hash from every other use of SHA-256 here.
const groupRefInfo = "tpp/v1/group-ref"

// GroupRef is a stable name for a group that says nothing about it: the
// first 16 hex characters of SHA-256 over "tpp/v1/group-ref" and the group
// id. Group ids are bearer credentials on the wire (State), so a UI, a URL
// and a log get this instead.
func GroupRef(groupID string) string {
	if groupID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(groupRefInfo + groupID))
	return hex.EncodeToString(sum[:8])
}

// ActiveRef is the active group's ref, or "" when this device is in no group.
func (c *Client) ActiveRef() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.InGroup() {
		return ""
	}
	return GroupRef(c.state.GroupID)
}

// ParkedGroups lists the parked groups, oldest parked first. A parked record
// of the active group — left behind by a switch that was interrupted — is
// not listed.
func (c *Client) ParkedGroups() []ParkedGroup {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ParkedGroup, 0, len(c.parked))
	for _, s := range c.parked {
		if s.GroupID == c.state.GroupID {
			continue
		}
		out = append(out, ParkedGroup{Ref: GroupRef(s.GroupID), GroupName: s.GroupName, ServerURL: s.ServerURL})
	}
	return out
}

// ParkActive sets the active group aside and leaves this client in no group,
// with a new keypair, ready to create a group or pair into one: adding a
// group is leaving one without deleting its keys. A group this device can no
// longer use — it was revoked, or the admin deleted the group — is discarded
// instead, as Forget would. With no active group it does nothing.
func (c *Client) ParkActive() error {
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()

	if !c.InGroup() {
		return nil
	}
	c.halt("this device is setting its group aside")

	c.mu.Lock()
	outgoing, dead := c.state, c.revokedErr != nil
	parked := c.parked
	c.mu.Unlock()

	if !dead {
		next := withGroup(parked, outgoing)
		if err := c.saveParked(next); err != nil {
			c.restart(outgoing)
			return err
		}
		c.mu.Lock()
		c.parked = next
		c.mu.Unlock()
	}

	fresh, err := c.freshState()
	if err != nil {
		c.restart(outgoing)
		return err
	}
	c.restart(fresh)
	if dead {
		c.logger.Info("this device's group was revoked or deleted; it was discarded, not parked",
			"group", GroupRef(outgoing.GroupID))
	} else {
		c.logger.Info("this device set its group aside", "group", GroupRef(outgoing.GroupID))
	}
	return c.persist()
}

// SwitchGroup makes the parked group ref names the active one. The active
// group is parked in its place, unless this device can no longer use it, in
// which case it is discarded as Forget would.
//
// It stops the client first, as closing the app would, and leaves it
// offline: the caller connects, as a launch with that group would. The
// writes are ordered so that an interruption leaves a group stored twice,
// never lost (/spec/crypto.md §10.1): the parked list gains the outgoing
// group, the active entry takes the incoming one, and only then does the
// parked list lose it.
//
// The device name is the installation's, so the incoming group takes the
// name this client is using now.
func (c *Client) SwitchGroup(ref string) error {
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.parkedLocked {
		c.mu.Unlock()
		return ErrParkedUnreadable
	}
	idx := indexOfRef(c.parked, ref)
	if idx < 0 || c.parked[idx].GroupID == c.state.GroupID {
		c.mu.Unlock()
		return ErrUnknownGroup
	}
	c.mu.Unlock()

	c.halt("this device is switching groups")

	c.mu.Lock()
	outgoing, dead := c.state, c.revokedErr != nil
	incoming := c.parked[idx]
	both := c.parked
	c.mu.Unlock()
	incoming.DeviceName = outgoing.DeviceName

	// 1. The outgoing group joins the parked list; the incoming one is still
	// in it.
	if outgoing.InGroup() && !dead {
		both = withGroup(both, outgoing)
		if err := c.saveParked(both); err != nil {
			c.restart(outgoing)
			return err
		}
	}
	// 2. The incoming group becomes the active entry.
	if c.stateStore != nil {
		if err := SaveState(c.stateStore, c.stateName, incoming); err != nil {
			c.mu.Lock()
			c.parked = both
			c.mu.Unlock()
			c.restart(outgoing)
			return err
		}
	}
	// 3. The incoming group leaves the parked list. A failure here costs
	// nothing but a duplicate, which the next start removes.
	rest := withoutGroup(both, incoming.GroupID)
	if err := c.saveParked(rest); err != nil {
		c.logger.Warn("the group switched, but its parked copy could not be removed yet",
			"group", GroupRef(incoming.GroupID), "error", err)
	}
	c.mu.Lock()
	c.parked = rest
	c.mu.Unlock()
	c.restart(incoming)

	c.logger.Info("this device switched groups",
		"from", GroupRef(outgoing.GroupID), "to", GroupRef(incoming.GroupID), "discarded", dead)
	return nil
}

// RemoveParked deletes a parked group's record, and with it the group key
// and the device key this installation held for it. Like Forget it is
// local: the relay still lists the device until another member revokes it.
// The active group is not a parked group and cannot be removed this way.
func (c *Client) RemoveParked(ref string) error {
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()

	c.mu.Lock()
	if c.parkedLocked {
		c.mu.Unlock()
		return ErrParkedUnreadable
	}
	idx := indexOfRef(c.parked, ref)
	if idx < 0 || c.parked[idx].GroupID == c.state.GroupID {
		c.mu.Unlock()
		return ErrUnknownGroup
	}
	next := withoutGroup(c.parked, c.parked[idx].GroupID)
	c.mu.Unlock()

	if err := c.saveParked(next); err != nil {
		return err
	}
	c.mu.Lock()
	c.parked = next
	c.mu.Unlock()
	c.logger.Info("a parked group was removed from this device", "group", ref)
	return nil
}

// loadParked reads the parked groups at start, and removes a parked copy of
// the active group that an interrupted switch left behind. It never fails the
// client: an unreadable record is reported and left alone, and the active
// group works without it.
func (c *Client) loadParked() {
	if c.stateStore == nil {
		return
	}
	raw, err := c.stateStore.Load(c.parkedName)
	if errors.Is(err, keystore.ErrNotFound) {
		return
	}
	var rec parkedRecord
	if err == nil {
		err = json.Unmarshal(raw, &rec)
		if err == nil && rec.Version != parkedVersion {
			err = fmt.Errorf("record version %d, this build reads %d", rec.Version, parkedVersion)
		}
	}
	if err != nil {
		c.logger.Error("the parked groups could not be read; they are kept as they are", "error", err)
		c.mu.Lock()
		c.parkedLocked = true
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	active := c.state.GroupID
	c.mu.Unlock()
	groups := rec.Groups
	if active != "" && indexOfGroup(groups, active) >= 0 {
		groups = withoutGroup(groups, active)
		if err := c.saveParked(groups); err != nil {
			c.logger.Warn("a parked copy of the active group could not be removed", "error", err)
		}
	}
	c.mu.Lock()
	c.parked = groups
	c.mu.Unlock()
}

// saveParked writes the parked list. An empty list deletes the entry, so an
// installation that never had a second group has nothing extra stored.
func (c *Client) saveParked(groups []State) error {
	c.mu.Lock()
	locked := c.parkedLocked
	c.mu.Unlock()
	if locked {
		return ErrParkedUnreadable
	}
	if c.stateStore == nil {
		return nil
	}
	if len(groups) == 0 {
		if err := c.stateStore.Delete(c.parkedName); err != nil {
			return fmt.Errorf("tppclient: delete the parked groups: %w", err)
		}
		return nil
	}
	raw, err := json.Marshal(parkedRecord{Version: parkedVersion, Groups: groups})
	if err != nil {
		return fmt.Errorf("tppclient: encode the parked groups: %w", err)
	}
	if err := c.stateStore.Save(c.parkedName, raw); err != nil {
		return fmt.Errorf("tppclient: store the parked groups: %w", err)
	}
	return nil
}

// withGroup returns a copy of groups with s in it, replacing an older record
// of the same group.
func withGroup(groups []State, s State) []State {
	out := withoutGroup(groups, s.GroupID)
	return append(out, s)
}

// withoutGroup returns a copy of groups without the group whose id is given.
func withoutGroup(groups []State, groupID string) []State {
	out := make([]State, 0, len(groups)+1)
	for _, g := range groups {
		if g.GroupID != groupID {
			out = append(out, g)
		}
	}
	return out
}

func indexOfGroup(groups []State, groupID string) int {
	for i, g := range groups {
		if g.GroupID == groupID {
			return i
		}
	}
	return -1
}

func indexOfRef(groups []State, ref string) int {
	if ref == "" {
		return -1
	}
	for i, g := range groups {
		if GroupRef(g.GroupID) == ref {
			return i
		}
	}
	return -1
}
