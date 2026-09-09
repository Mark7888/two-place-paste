// Command gopeer is the Go half of the mobile client's interop test.
//
// It drives `pkg/tppclient` — the same client core the desktop app is built on
// — from a line protocol on stdin, so that a test written in JavaScript can
// pair the two implementations against a real relay and check that what one
// encrypts the other decrypts. That is the acceptance criterion ROADMAP P7
// states as "a scripted test pairs the RN client with a Go client against a
// local server", and it is the only thing that can catch the two crypto
// implementations disagreeing about something the vectors do not pin — a
// pairing payload's encoding, an entry id's alphabet, an epoch's width on the
// wire.
//
// It is a test tool, not product code: a standalone module, deliberately absent
// from /go.work (a shared touchpoint owned by ROADMAP P0), in the same way
// spec/vectors/gen is. Run it with GOWORK=off.
//
// Commands, one per line on stdin. Every one answers with a single JSON line.
//
//	create <creation-url>   create a group and connect
//	pair                    start a pairing and print the payload
//	await-join              wait for a device to join the pending pairing
//	put <text>              write a text entry
//	latest                  read the group's latest entry
//	devices                 list the group roster
//	revoke <device-id>      prepare and confirm a revocation
//	epoch                   report this client's epoch
//	quit                    close and exit
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
)

func main() {
	if err := run(); err != nil {
		reply(map[string]any{"ok": false, "error": err.Error()})
		os.Exit(1)
	}
}

func run() error {
	name := os.Getenv("TPP_DEVICE_NAME")
	if name == "" {
		name = "Go peer"
	}
	dir, err := os.MkdirTemp("", "tpp-gopeer-")
	if err != nil {
		return fmt.Errorf("create the peer's state directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	store, err := keystore.Open(keystore.Options{
		Dir:        dir,
		Passphrase: []byte("interop"),
		ForceFile:  true,
	})
	if err != nil {
		return fmt.Errorf("open the peer's keystore: %w", err)
	}

	client, err := tppclient.New(tppclient.Options{
		DeviceName: name,
		Keystore:   store,
		// The peer's own logging goes to stderr and is not part of the
		// protocol this tool speaks on stdout.
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return fmt.Errorf("build the peer client: %w", err)
	}
	defer func() { _ = client.Close() }()

	var pending *tppclient.Invitation
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)

	for scanner.Scan() {
		command, argument, _ := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if command == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

		switch command {
		case "create":
			if err := client.CreateGroup(ctx, argument); err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{"ok": true, "epoch": client.Epoch(), "device_id": client.State().DeviceID})

		case "pair":
			invitation, err := client.StartPairing(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			pending = invitation
			reply(map[string]any{"ok": true, "payload": invitation.Payload})

		case "await-join":
			if pending == nil {
				reply(map[string]any{"ok": false, "error": "no pairing is in progress"})
				break
			}
			device, err := pending.Wait(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			pending = nil
			reply(map[string]any{"ok": true, "device_id": device.ID, "device_name": device.Name})

		case "put":
			meta, err := client.PutEntry(ctx, tppclient.Item{
				ContentType: "text/plain; charset=utf-8",
				Body:        []byte(argument),
			})
			if err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{"ok": true, "entry_id": meta.ID, "epoch": meta.Epoch, "size": meta.Size})

		case "latest":
			item, err := client.GetLatest(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{
				"ok":           true,
				"content_type": item.ContentType,
				"filename":     item.Filename,
				"body":         string(item.Body),
				"entry_id":     item.Meta.ID,
				"epoch":        item.Meta.Epoch,
			})

		case "devices":
			roster, err := client.Devices(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			devices := make([]map[string]any, 0, len(roster.Devices))
			for _, d := range roster.Devices {
				devices = append(devices, map[string]any{"id": d.ID, "name": d.Name, "this": d.This})
			}
			reply(map[string]any{"ok": true, "epoch": roster.Epoch, "devices": devices})

		case "revoke":
			revocation, err := client.Revoke(ctx, argument)
			if err != nil {
				reply(failure(err))
				break
			}
			// The confirmation a user gives on a screen is given here by the
			// test: nothing is revoked until this call.
			remaining, err := revocation.Confirm(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{"ok": true, "epoch": remaining.Epoch, "remaining": len(remaining.Devices)})

		case "epoch":
			reply(map[string]any{"ok": true, "epoch": client.Epoch()})

		case "quit":
			reply(map[string]any{"ok": true})
			cancel()
			return nil

		default:
			reply(map[string]any{"ok": false, "error": "unknown command " + command})
		}
		cancel()
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("read commands: %w", err)
	}
	return nil
}

func failure(err error) map[string]any {
	return map[string]any{"ok": false, "error": err.Error()}
}

// reply writes one JSON line. stdout carries nothing else, so the test can
// read a line per command.
func reply(value map[string]any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stdout, `{"ok":false,"error":"encode reply"}%s`, "\n")
		return
	}
	fmt.Fprintf(os.Stdout, "%s\n", encoded)
}
