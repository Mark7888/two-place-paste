package main

import (
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/service"
	"github.com/Mark7888/two-place-paste/desktop/internal/tray"
)

func TestTrayStatus(t *testing.T) {
	t.Parallel()

	up := service.Activity{InGroup: true, Connected: true, EverConnected: true}
	tests := []struct {
		name string
		a    service.Activity
		want tray.Status
	}{
		{name: "no group", a: service.Activity{}, want: tray.StatusUnpaired},
		{name: "starting", a: service.Activity{InGroup: true}, want: tray.StatusConnecting},
		{name: "connected", a: up, want: tray.StatusConnected},
		{name: "dropped", a: service.Activity{InGroup: true, EverConnected: true}, want: tray.StatusDisconnected},
		{name: "revoked", a: service.Activity{InGroup: true, EverConnected: true, Revoked: true}, want: tray.StatusDisconnected},
		{name: "uploading", a: with(up, func(a *service.Activity) { a.Uploading = true }), want: tray.StatusUploading},
		{name: "downloading", a: with(up, func(a *service.Activity) { a.Downloading = true }), want: tray.StatusDownloading},
		{name: "both", a: with(up, func(a *service.Activity) { a.Uploading, a.Downloading = true, true }), want: tray.StatusSyncing},
		{name: "upload held back", a: with(up, func(a *service.Activity) { a.Skipped = "upload" }), want: tray.StatusSkippedUpload},
		{name: "download held back", a: with(up, func(a *service.Activity) { a.Skipped = "download" }), want: tray.StatusSkippedDownload},
		{name: "a transfer outranks a held-back one", a: with(up, func(a *service.Activity) { a.Skipped, a.Downloading = "upload", true }), want: tray.StatusDownloading},
		{name: "a lost connection outranks a held-back one", a: service.Activity{InGroup: true, EverConnected: true, Skipped: "upload"}, want: tray.StatusDisconnected},
		{name: "paused", a: with(up, func(a *service.Activity) { a.Paused = true }), want: tray.StatusPaused},
		{name: "a pause outranks what it held back", a: with(up, func(a *service.Activity) { a.Paused, a.Skipped = true, "download" }), want: tray.StatusPaused},
		{name: "a manual transfer outranks a pause", a: with(up, func(a *service.Activity) { a.Paused, a.Uploading = true, true }), want: tray.StatusUploading},
		{name: "a lost connection outranks a pause", a: service.Activity{InGroup: true, EverConnected: true, Paused: true}, want: tray.StatusDisconnected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := trayStatus(tt.a); got != tt.want {
				t.Errorf("trayStatus(%+v) = %v, want %v", tt.a, got, tt.want)
			}
		})
	}
}

func with(a service.Activity, change func(*service.Activity)) service.Activity {
	change(&a)
	return a
}
