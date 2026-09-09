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

	// CreateGroup turns an admin creation URL into a group (SPEC §3.1).
	CreateGroup(ctx context.Context, creationURL string) error

	// Settings returns the settings screen's model.
	Settings(ctx context.Context) SettingsView

	// UpdateSettings applies a partial change.
	UpdateSettings(ctx context.Context, p SettingsPatch) (SettingsView, error)

	// Subscribe returns a channel of events and a function that stops it. The
	// channel is closed when the subscription ends.
	Subscribe() (<-chan Event, func())
}
