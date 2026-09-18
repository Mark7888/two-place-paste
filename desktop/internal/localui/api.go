package localui

import "context"

// API is everything the UI can ask the service to do. It is declared here,
// where it is consumed, so this package can be tested against a stub with no
// relay, no clipboard and no keystore in sight.
type API interface {
	// Status is the sync screen's model.
	Status(ctx context.Context) Status

	// Sync moves the latest entry in one direction (SPEC §6).
	Sync(ctx context.Context, d Direction) (SyncResult, error)

	// History lists entry metadata. It is only ever pulled, never pushed.
	History(ctx context.Context, q HistoryQuery) (HistoryPage, error)

	// CopyEntry fetches one entry, decrypts it and puts it on the local
	// clipboard — what a history browser does when the user picks an entry.
	CopyEntry(ctx context.Context, entryID string) (SyncResult, error)

	// EntryPreview decrypts one entry and renders it for display only. It
	// never touches the clipboard — that is CopyEntry's job, and keeping the
	// two apart is what lets a user look at an entry without replacing what
	// they have copied.
	EntryPreview(ctx context.Context, entryID string) (EntryPreview, error)

	// Devices returns the group roster (SPEC §3.3 step 1).
	Devices(ctx context.Context) (RosterView, error)

	// PrepareRevoke gathers what the confirmation dialog must show and
	// changes nothing.
	PrepareRevoke(ctx context.Context, deviceID string) (RevokePlan, error)

	// ConfirmRevoke carries out a plan the user confirmed.
	ConfirmRevoke(ctx context.Context, planID string) (RosterView, error)

	// StartPairing mints a pairing token and returns the payload to show.
	StartPairing(ctx context.Context) (Invite, error)

	// JoinPairing joins a group with a payload pasted from another device.
	JoinPairing(ctx context.Context, payload string) error

	// StartOffer shows a code for a member of some group to accept, which is
	// how a device with no group key pairs when it is the one with a screen
	// the user is looking at (docs/plans/joiner-emitted-pairing.md).
	StartOffer(ctx context.Context, serverURL string) (OfferView, error)

	// CancelOffer withdraws the offer this device is showing.
	CancelOffer(ctx context.Context) error

	// PrepareAcceptOffer decodes an offer and returns what the confirmation
	// dialog must show. It changes nothing.
	PrepareAcceptOffer(ctx context.Context, code string) (OfferPlan, error)

	// ConfirmAcceptOffer admits the device a plan describes. It is the only
	// way a device is admitted this way.
	ConfirmAcceptOffer(ctx context.Context, planID string) (DeviceView, error)

	// CreateGroup turns an admin creation URL into a group (SPEC §3.1).
	CreateGroup(ctx context.Context, creationURL string) error

	// Forget disconnects this device from its group and deletes the keys it
	// held. It is local: nothing is removed from the relay, which still lists
	// this device until another one revokes it (SPEC §3.3).
	Forget(ctx context.Context) error

	// Settings returns the settings screen's model.
	Settings(ctx context.Context) SettingsView

	// UpdateSettings applies a partial change.
	UpdateSettings(ctx context.Context, p SettingsPatch) (SettingsView, error)

	// Subscribe returns a channel of events and a function that stops it. The
	// channel is closed when the subscription ends.
	Subscribe() (<-chan Event, func())
}
