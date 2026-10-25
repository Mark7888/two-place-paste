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
//	read-offer <code>       read a joiner-emitted code; changes nothing
//	accept-offer            admit the device the last read-offer described
//	put <text>              write a text entry
//	latest                  read the group's latest entry
//	put-large <bytes>       write a binary entry of that size, in a fixed pattern
//	latest-digest           read the latest entry; report its length and SHA-256
//	devices                 list the group roster
//	revoke <device-id>      prepare and confirm a revocation
//	epoch                   report this client's epoch
//	join <payload>          join a group with a pairing payload
//	store-save <json>       save {name, description, tags, content_type, filename, body} to the store
//	store-list              list the store, opened
//	store-fetch <item-id>   fetch an item; report its body, length and SHA-256
//	store-edit <json>       edit {id, name, description, tags}
//	store-delete <item-id>  delete an item
//	quit                    close and exit
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
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

	var (
		pending      *tppclient.Invitation
		pendingOffer *tppclient.OfferAcceptance
	)
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

		case "read-offer":
			// The consent gate of docs/internal/plans/joiner-emitted-pairing.md §5,
			// across the process boundary: reading a code changes nothing and
			// returns only what a dialog would render.
			accept, err := client.PrepareAcceptOffer(ctx, argument)
			if err != nil {
				reply(failure(err))
				break
			}
			pendingOffer = accept
			reply(map[string]any{
				"ok":          true,
				"device_name": accept.DeviceName,
				"fingerprint": accept.Fingerprint,
			})

		case "accept-offer":
			if pendingOffer == nil {
				reply(map[string]any{"ok": false, "error": "no offer has been read"})
				break
			}
			// The confirmation a user gives on a screen is given here by the
			// test: no key is wrapped until this call.
			device, err := pendingOffer.Confirm(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			pendingOffer = nil
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

		case "history":
			// Each entry's id and what its sealed header says, so the phone's
			// headers are checked by an independent implementation.
			page, err := client.GetHistory(ctx, tppclient.HistoryQuery{Limit: 50})
			if err != nil {
				reply(failure(err))
				break
			}
			entries := make([]map[string]any, 0, len(page.Entries))
			for _, m := range page.Entries {
				e := map[string]any{"entry_id": m.ID}
				if m.Header != nil {
					e["content_type"] = m.Header.ContentType
					e["filename"] = m.Header.Filename
					e["bytes"] = m.Header.Size
				}
				entries = append(entries, e)
			}
			reply(map[string]any{"ok": true, "entries": entries})

		case "put-large":
			// A body of the given size in a pattern the phone can rebuild,
			// so the test compares bytes without shipping them twice.
			n, err := strconv.Atoi(argument)
			if err != nil || n <= 0 {
				reply(map[string]any{"ok": false, "error": "put-large needs a positive size"})
				break
			}
			body := make([]byte, n)
			for i := range body {
				body[i] = byte(i*31 + 7)
			}
			meta, err := client.PutEntry(ctx, tppclient.Item{ContentType: "application/octet-stream", Filename: "large.bin", Body: body})
			if err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{"ok": true, "entry_id": meta.ID, "size": meta.Size})

		case "latest-digest":
			// The latest entry's plaintext by SHA-256 and length: how a large
			// entry the phone wrote is checked without echoing it back.
			item, err := client.GetLatest(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			sum := sha256.Sum256(item.Body)
			reply(map[string]any{
				"ok":       true,
				"entry_id": item.Meta.ID,
				"length":   len(item.Body),
				"sha256":   hex.EncodeToString(sum[:]),
				"filename": item.Filename,
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

		case "join":
			if err := client.JoinPairing(ctx, argument); err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{"ok": true, "epoch": client.Epoch(), "device_id": client.State().DeviceID})

		case "store-save":
			var req struct {
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Tags        []string `json:"tags"`
				ContentType string   `json:"content_type"`
				Filename    string   `json:"filename"`
				Body        string   `json:"body"`
			}
			if err := json.Unmarshal([]byte(argument), &req); err != nil {
				reply(failure(err))
				break
			}
			it, err := client.SaveToStore(ctx,
				tppclient.Item{ContentType: req.ContentType, Filename: req.Filename, Body: []byte(req.Body)},
				tppclient.StoreDetails{Name: req.Name, Description: req.Description, Tags: req.Tags})
			if err != nil {
				reply(failure(err))
				break
			}
			reply(map[string]any{"ok": true, "id": it.ID, "wrap_epoch": it.WrapEpoch})

		case "store-list":
			listing, err := client.ListStore(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			items := make([]map[string]any, 0, len(listing.Items))
			for _, it := range listing.Items {
				tags := it.Tags
				if tags == nil {
					tags = []string{}
				}
				items = append(items, map[string]any{
					"id": it.ID, "name": it.Name, "description": it.Description, "tags": tags,
					"content_type": it.ContentType, "filename": it.Filename, "body_size": it.BodySize,
					"sha256": hex.EncodeToString(it.SHA256[:]), "meta_rev": it.MetaRev, "wrap_epoch": it.WrapEpoch,
				})
			}
			reply(map[string]any{"ok": true, "items": items, "unreadable": len(listing.Unreadable)})

		case "store-fetch", "store-edit", "store-delete":
			// The client opens an item from a listing, which carries its keys.
			id := argument
			var edit struct {
				ID          string   `json:"id"`
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Tags        []string `json:"tags"`
			}
			if command == "store-edit" {
				if err := json.Unmarshal([]byte(argument), &edit); err != nil {
					reply(failure(err))
					break
				}
				id = edit.ID
			}
			if command == "store-delete" {
				if err := client.DeleteStoreItem(ctx, id); err != nil {
					reply(failure(err))
					break
				}
				reply(map[string]any{"ok": true})
				break
			}
			listing, err := client.ListStore(ctx)
			if err != nil {
				reply(failure(err))
				break
			}
			var found *tppclient.StoreItem
			for i := range listing.Items {
				if listing.Items[i].ID == id {
					found = &listing.Items[i]
				}
			}
			if found == nil {
				reply(map[string]any{"ok": false, "error": "no such readable item " + id})
				break
			}
			if command == "store-edit" {
				edited, err := client.EditStoreItem(ctx, *found,
					tppclient.StoreDetails{Name: edit.Name, Description: edit.Description, Tags: edit.Tags})
				if err != nil {
					reply(failure(err))
					break
				}
				reply(map[string]any{"ok": true, "meta_rev": edited.MetaRev})
				break
			}
			item, err := client.FetchStoreItem(ctx, *found)
			if err != nil {
				reply(failure(err))
				break
			}
			sum := sha256.Sum256(item.Body)
			reply(map[string]any{
				"ok": true, "body": string(item.Body), "length": len(item.Body),
				"sha256": hex.EncodeToString(sum[:]), "content_type": item.ContentType, "filename": item.Filename,
			})

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
