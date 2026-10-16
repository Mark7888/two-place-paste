package update

import (
	"context"
	"testing"
)

// reopen builds a second manager over the first one's state file and
// keystore: the service as the next launch finds it.
func (n *nightlyHarness) reopen(t *testing.T) *Manager {
	t.Helper()
	m, err := New(n.m.opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTheTokenExpiryIsKeptAcrossLaunches(t *testing.T) {
	n := newNightlyHarness(t)
	n.actions.expires = "2027-01-05 14:31:04 UTC"
	if _, err := n.m.SetUpdateToken(context.Background(), testToken); err != nil {
		t.Fatal(err)
	}

	v := n.reopen(t).Status(context.Background()).Token
	if v.ExpiresAt == nil || v.ExpiresAt.Year() != 2027 || v.ExpiresAt.Month() != 1 || v.ExpiresAt.Day() != 5 {
		t.Fatalf("token view after a restart = %+v; want it to expire on 2027-01-05", v)
	}
}

func TestATokenWithoutAnExpirySaysSo(t *testing.T) {
	n := newNightlyHarness(t)
	n.actions.expires = "2027-01-05 14:31:04 UTC"
	if _, err := n.m.SetUpdateToken(context.Background(), testToken); err != nil {
		t.Fatal(err)
	}

	// Replaced by a token made with no expiration: the old date must not
	// linger on it.
	n.actions.expires = ""
	v, err := n.m.SetUpdateToken(context.Background(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Token.NoExpiry || v.Token.ExpiresAt != nil {
		t.Fatalf("token view = %+v; want no expiry", v.Token)
	}
	if v := n.reopen(t).Status(context.Background()).Token; !v.NoExpiry || v.ExpiresAt != nil {
		t.Fatalf("token view after a restart = %+v; want no expiry", v)
	}
}

func TestAnUncheckedTokenLearnsItsExpiry(t *testing.T) {
	n := newNightlyHarness(t)
	n.actions.expires = "2027-03-01 09:00:00 UTC"
	// Saved by a build that did not record the expiry.
	if err := n.secrets.Save(tokenSecret, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	m := n.reopen(t)
	if v := m.Status(context.Background()).Token; !v.Set || v.ExpiresAt != nil || v.NoExpiry {
		t.Fatalf("token view before GitHub was asked = %+v; want set, expiry unknown", v)
	}

	m.learnTokenExpiry(context.Background())
	if v := m.Status(context.Background()).Token; v.ExpiresAt == nil || v.ExpiresAt.Month() != 3 {
		t.Fatalf("token view after asking = %+v; want it to expire in March 2027", v)
	}
}

func TestRemovingTheTokenForgetsItsExpiry(t *testing.T) {
	n := newNightlyHarness(t)
	n.actions.expires = "2027-01-05 14:31:04 UTC"
	if _, err := n.m.SetUpdateToken(context.Background(), testToken); err != nil {
		t.Fatal(err)
	}
	if _, err := n.m.DeleteUpdateToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A new token saved some other way must not inherit the old date.
	if err := n.secrets.Save(tokenSecret, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if v := n.reopen(t).Status(context.Background()).Token; v.ExpiresAt != nil || v.NoExpiry {
		t.Fatalf("token view = %+v; want the expiry unknown", v)
	}
}
