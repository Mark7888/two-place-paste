package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/apikey"
	"github.com/Mark7888/two-place-paste/server/internal/config"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// runAPIKey implements `tpp apikey create|list|revoke`
// (docs/internal/plans/admin-rest-api.md §4.5): the admin API's keys, from
// the relay's own host, for setups that never open the admin page. It reads
// the relay's configuration, works on the same Redis, and exits; a running
// relay sees the change on its next request, because it reads keys from
// Redis every time.
func runAPIKey(args []string) error {
	if len(args) == 0 || isFlag(args[0]) || args[0] == "help" {
		apiKeyUsage(os.Stderr)
		if len(args) > 0 && args[0] == "help" {
			return nil
		}
		return errors.New("apikey: name a subcommand: create, list or revoke")
	}

	cfg, err := config.Load(config.Options{})
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx := context.Background()
	rdb, err := openRedis(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	st := store.NewRedisStore(rdb)
	return apiKeyCommand(ctx, st, st, args, os.Stdout, os.Stderr, time.Now().UTC())
}

// auditLog is where the command records what it did.
type auditLog interface {
	AppendAudit(ctx context.Context, e store.AuditEntry) error
}

// apiKeyCommand runs one apikey subcommand against keys. A new key is the
// only thing written to stdout, so `KEY=$(tpp apikey create …)` works;
// everything meant for a person goes to stderr.
func apiKeyCommand(ctx context.Context, keys store.APIKeys, audit auditLog, args []string, stdout, stderr io.Writer, now time.Time) error {
	sub, args := args[0], args[1:]
	switch sub {
	case "create":
		return apiKeyCreate(ctx, keys, audit, args, stdout, stderr, now)
	case "list":
		if len(args) > 0 {
			return fmt.Errorf("apikey list takes no arguments, got %q", args[0])
		}
		return apiKeyList(ctx, keys, stdout, now)
	case "revoke":
		if len(args) != 1 {
			return errors.New("apikey revoke takes one argument: the key's id, as `tpp apikey list` shows it")
		}
		return apiKeyRevoke(ctx, keys, audit, args[0], stderr, now)
	default:
		apiKeyUsage(stderr)
		return fmt.Errorf("apikey: unknown subcommand %q", sub)
	}
}

func apiKeyCreate(ctx context.Context, keys store.APIKeys, audit auditLog, args []string, stdout, stderr io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("apikey create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "what the key is for, e.g. \"backup script\" (required)")
	scopes := fs.String("scope", "", "comma-separated scopes: read, tokens, admin (required)")
	expires := fs.String("expires", strconv.Itoa(apikey.DefaultExpiryDays), "lifetime in days: 30, 90 or 365, or never")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("apikey create: parse flags: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("apikey create: unexpected argument %q", fs.Arg(0))
	}
	days := 0
	if e := strings.TrimSuffix(strings.TrimSpace(*expires), "d"); e != "never" {
		n, err := strconv.Atoi(e)
		if err != nil {
			return fmt.Errorf("apikey create: %w", apikey.ErrExpiry)
		}
		days = n
	}
	var list []string
	for s := range strings.SplitSeq(*scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, s)
		}
	}

	key, rec, err := apikey.Create(ctx, keys, apikey.New{Name: *name, Scopes: list, ExpiryDays: days, CreatedBy: "cli"}, now)
	if errors.Is(err, store.ErrAPIKeyLimit) {
		return fmt.Errorf("apikey create: there are already %d keys; revoke one first", store.MaxAPIKeys)
	}
	if err != nil {
		return fmt.Errorf("apikey create: %w", err)
	}
	recordCLI(ctx, audit, stderr, "apikey.create", rec, keyDetail(rec), now)

	fmt.Fprintf(stderr, "Created API key %s (%s) with scope %s, %s.\n"+
		"It is shown once, below, and stored nowhere. Copy it now.\n",
		rec.ID, rec.Name, strings.Join(rec.Scopes, ", "), expiryWords(rec.ExpiresAt))
	if _, err := fmt.Fprintln(stdout, key); err != nil {
		return fmt.Errorf("apikey create: write key: %w", err)
	}
	return nil
}

func apiKeyList(ctx context.Context, keys store.APIKeys, stdout io.Writer, now time.Time) error {
	list, err := keys.ListAPIKeys(ctx)
	if err != nil {
		return fmt.Errorf("apikey list: %w", err)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSCOPES\tCREATED\tEXPIRES\tLAST USED\tSTATUS")
	for _, k := range list {
		status := "active"
		if k.Expired(now) {
			status = "expired"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			k.ID, k.Name, strings.Join(k.Scopes, ","), day(k.CreatedAt), dayOr(k.ExpiresAt, "never"), dayOr(k.LastUsedAt, "never"), status)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("apikey list: write: %w", err)
	}
	return nil
}

func apiKeyRevoke(ctx context.Context, keys store.APIKeys, audit auditLog, id string, stderr io.Writer, now time.Time) error {
	rec, err := keys.GetAPIKey(ctx, id)
	if err == nil {
		err = keys.DeleteAPIKey(ctx, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("apikey revoke: there is no key %q; `tpp apikey list` shows them", id)
	}
	if err != nil {
		return fmt.Errorf("apikey revoke: %w", err)
	}
	recordCLI(ctx, audit, stderr, "apikey.revoke", rec, "", now)
	fmt.Fprintf(stderr, "Revoked API key %s (%s). It stops working on its next request.\n", rec.ID, rec.Name)
	return nil
}

// recordCLI adds an entry to the admin audit log. A failure is reported and
// does not undo the change, as on the admin page.
func recordCLI(ctx context.Context, audit auditLog, stderr io.Writer, action string, k store.APIKey, detail string, now time.Time) {
	err := audit.AppendAudit(ctx, store.AuditEntry{
		At: now, Action: action, Target: k.Name + " (" + k.ID + ")", Detail: detail, Actor: "cli",
	})
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not record this in the audit log: %v\n", err)
	}
}

// keyDetail describes a new key for the audit log: scopes and expiry, never
// the key.
func keyDetail(k store.APIKey) string {
	return strings.Join(k.Scopes, ", ") + "; " + expiryWords(k.ExpiresAt)
}

func expiryWords(t time.Time) string {
	if t.IsZero() {
		return "never expires"
	}
	return "expires " + day(t)
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

func dayOr(t time.Time, zero string) string {
	if t.IsZero() {
		return zero
	}
	return day(t)
}

func apiKeyUsage(w io.Writer) {
	fmt.Fprint(w, `usage:
  tpp apikey create --name NAME --scope read|tokens|admin[,...] [--expires 30|90|365|never]
  tpp apikey list
  tpp apikey revoke ID

API keys authenticate the admin REST API under /api/v1/. create prints the
new key on stdout, once; it is stored nowhere. Keys can also be managed on
the admin page's API keys screen.
`)
}
