package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/desktop/internal/sensitive"
)

// Sensitive content (docs/internal/plans/auto-upload-filters.md).
//
// A sensitive clipboard change is never uploaded by auto-watch, and an upload
// the user starts by hand is refused with a plan the user confirms. The plan
// holds the digest of what was refused, not the content and not a hash the
// UI ever sees: the UI and the tray carry only its random id.

// pushPlanTTL bounds how long a "push anyway?" question stays answerable. Long
// enough to read a dialog, short enough that an answer is about the copy the
// question was asked for.
const pushPlanTTL = 2 * time.Minute

// maxFilterSampleBytes bounds the sample a filter test looks at.
const maxFilterSampleBytes = 1 << 20

type pushPlan struct {
	digest    [32]byte
	expiresAt time.Time
}

// verdictCache remembers the last classification. Status asks on every
// refresh and the watcher on every change, and with an exact filter set a
// classification costs an argon2id hash; the clipboard rarely changes in
// between.
type verdictCache struct {
	digest     [32]byte
	classifier *sensitive.Classifier
	verdict    sensitive.Verdict
	ok         bool
}

// buildClassifier makes the classifier the settings describe. Settings are
// validated when loaded and saved, so failing here means a bug or a settings
// file edited under a running service; the fallback still honours the OS
// markers and the detectors rather than protecting nothing.
func buildClassifier(set config.Settings) (*sensitive.Classifier, error) {
	cfg, err := set.SensitiveConfig()
	if err == nil {
		var c *sensitive.Classifier
		if c, err = sensitive.New(cfg); err == nil {
			return c, nil
		}
	}
	fallback, ferr := sensitive.New(sensitive.Config{
		DetectOS:  set.SensitiveDetection(),
		Detectors: set.EnabledDetectors(),
	})
	if ferr != nil {
		return nil, errors.Join(err, ferr)
	}
	return fallback, fmt.Errorf("build the sensitive-content filters: %w", err)
}

// classify reports whether c is sensitive under the current settings.
func (s *Service) classify(c clipboard.Content) sensitive.Verdict {
	d := c.Digest()
	s.mu.Lock()
	cl := s.classifier
	if s.verdicts.ok && s.verdicts.digest == d && s.verdicts.classifier == cl {
		v := s.verdicts.verdict
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()

	// Outside the lock: a large text or an exact filter takes a while, and
	// nothing else should wait on it.
	v := cl.Classify(c)

	s.mu.Lock()
	if s.classifier == cl {
		s.verdicts = verdictCache{digest: d, classifier: cl, verdict: v, ok: true}
	}
	s.mu.Unlock()
	return v
}

// askToPush records a plan for content refused as sensitive and returns the
// error the UI and the tray turn into "push anyway?".
func (s *Service) askToPush(c clipboard.Content, v sensitive.Verdict) error {
	id, err := planID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sweepPushPlansLocked()
	s.pushPlans[id] = &pushPlan{digest: c.Digest(), expiresAt: s.now().Add(pushPlanTTL)}
	s.mu.Unlock()
	return &localui.SensitiveError{PlanID: id, Reason: v.Reason}
}

// confirmedPush reports whether planID is an unexpired plan for exactly this
// content. The plan is used up either way: an answer applies once.
func (s *Service) confirmedPush(planID string, c clipboard.Content) bool {
	if planID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepPushPlansLocked()
	p, ok := s.pushPlans[planID]
	delete(s.pushPlans, planID)
	return ok && p.digest == c.Digest()
}

func (s *Service) sweepPushPlansLocked() {
	now := s.now()
	for id, p := range s.pushPlans {
		if now.After(p.expiresAt) {
			delete(s.pushPlans, id)
		}
	}
}

// noteSensitive records that auto-watch kept a change on this device. It is
// quiet on purpose: no tray state and no notification, only a line on the
// sync screen.
func (s *Service) noteSensitive(reason string) {
	s.mu.Lock()
	s.sensitiveNote = &localui.SensitiveNote{Reason: reason, At: s.now()}
	// A held-back upload is about a copy this one replaced.
	if s.skipped != nil && s.skipped.Direction == "upload" {
		s.skipped = nil
	}
	s.mu.Unlock()
	s.activityChanged()
	s.hub.publish(localui.Event{Kind: localui.EventSensitive, Message: reason})
}

func (s *Service) clearSensitiveNote() {
	s.mu.Lock()
	s.sensitiveNote = nil
	s.mu.Unlock()
}

// RevealClipboard implements localui.API: the preview the status masked.
func (s *Service) RevealClipboard(ctx context.Context) (localui.ClipboardReveal, error) {
	c, err := s.clip.Read(ctx)
	switch {
	case errors.Is(err, clipboard.ErrEmpty):
		return localui.ClipboardReveal{}, nil
	case errors.Is(err, clipboard.ErrUnsupported):
		return localui.ClipboardReveal{}, localui.Errorf(http.StatusNotImplemented, err, "this build cannot reach a clipboard")
	case err != nil:
		return localui.ClipboardReveal{}, localui.Errorf(http.StatusInternalServerError, err, "the clipboard could not be read")
	}
	return localui.ClipboardReveal{Preview: clipboardPreview(c)}, nil
}

// TestFilters implements localui.API. The sample is classified against the
// draft the editor holds, so a match shows before anything is saved. Nothing
// about the sample is logged or kept.
func (s *Service) TestFilters(_ context.Context, t localui.FilterTest) (localui.FilterTestResult, error) {
	if len(t.Text) > maxFilterSampleBytes || len(t.Filename) > maxFilterSampleBytes {
		return localui.FilterTestResult{}, localui.Errorf(http.StatusRequestEntityTooLarge, nil,
			"a test sample is limited to 1 MB")
	}
	s.mu.Lock()
	set := s.settings.Clone()
	s.mu.Unlock()

	if t.Detectors != nil {
		if err := checkDetectors(*t.Detectors); err != nil {
			return localui.FilterTestResult{}, err
		}
		d := append([]string(nil), *t.Detectors...)
		set.Detectors = &d
	}
	if t.Filters != nil {
		salt := set.FilterSalt
		if salt == "" {
			// A test may hash an exact filter before any is saved. A salt
			// made for this test only is right for comparing the sample with
			// the draft, which is all a test does.
			var err error
			if salt, err = sensitive.NewSalt(); err != nil {
				return localui.FilterTestResult{}, fmt.Errorf("test the filters: %w", err)
			}
		}
		filters, err := resolveFilters(*t.Filters, set.Filters, salt)
		if err != nil {
			return localui.FilterTestResult{}, err
		}
		set.Filters, set.FilterSalt = filters, salt
	}
	cfg, err := set.SensitiveConfig()
	if err != nil {
		return localui.FilterTestResult{}, localui.Errorf(http.StatusBadRequest, err, "%s", filterMessage(err))
	}
	// The OS marker belongs to a real clipboard, not a typed sample.
	cfg.DetectOS = false
	cl, err := sensitive.New(cfg)
	if err != nil {
		return localui.FilterTestResult{}, localui.Errorf(http.StatusBadRequest, err, "%s", filterMessage(err))
	}

	out := localui.FilterTestResult{Matches: []localui.FilterMatch{}}
	if t.Text != "" {
		for _, v := range cl.Matches(clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(t.Text)}) {
			out.Matches = append(out.Matches, filterMatch(v))
		}
	}
	if t.Filename != "" {
		// A filename sample is checked as a copied file, so only the
		// filename filters look at it.
		c := clipboard.Content{ContentType: clipboard.TypeOctetStream, Filename: t.Filename}
		for _, v := range cl.Matches(c) {
			out.Matches = append(out.Matches, filterMatch(v))
		}
	}
	return out, nil
}

func filterMatch(v sensitive.Verdict) localui.FilterMatch {
	return localui.FilterMatch{Source: string(v.Source), Rule: v.Rule, Reason: v.Reason, Name: v.Name}
}

// checkDetectors refuses a detector name this build does not have. The
// settings file tolerates one (a newer version may have written it); a
// request from this build's own UI never should.
func checkDetectors(names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if _, ok := sensitive.LookupDetector(n); !ok {
			return localui.Errorf(http.StatusBadRequest, nil, "there is no built-in detector called %q", n)
		}
		if seen[n] {
			return localui.Errorf(http.StatusBadRequest, nil, "the detector %q is listed twice", n)
		}
		seen[n] = true
	}
	return nil
}

// resolveFilters turns the editor's list into stored filters. A new exact
// secret is hashed here and never kept; an exact filter sent without one
// keeps the hash saved under its id.
func resolveFilters(views []localui.FilterView, saved []sensitive.Filter, salt string) ([]sensitive.Filter, error) {
	if len(views) > sensitive.MaxFilters {
		return nil, localui.Errorf(http.StatusBadRequest, nil, "at most %d filters can be saved", sensitive.MaxFilters)
	}
	byID := make(map[string]sensitive.Filter, len(saved))
	for _, f := range saved {
		byID[f.ID] = f
	}
	var saltBytes []byte
	out := make([]sensitive.Filter, 0, len(views))
	for i, v := range views {
		f := sensitive.Filter{
			ID:              strings.TrimSpace(v.ID),
			Kind:            sensitive.Kind(v.Kind),
			Target:          sensitive.Target(v.Target),
			Value:           v.Value,
			CaseInsensitive: v.CaseInsensitive,
			Enabled:         v.Enabled,
			Label:           strings.TrimSpace(v.Label),
		}
		if f.ID == "" {
			id, err := sensitive.NewID()
			if err != nil {
				return nil, fmt.Errorf("name filter %d: %w", i+1, err)
			}
			f.ID = id
		}
		if f.Target == "" {
			f.Target = sensitive.TargetText
		}
		switch f.Kind {
		case sensitive.KindRegex, sensitive.KindExact:
			// A regex says (?i) itself; a secret is compared as typed.
			f.CaseInsensitive = false
		}
		if f.Kind == sensitive.KindExact {
			switch {
			case f.Value != "":
				if saltBytes == nil {
					b, err := sensitive.DecodeSalt(salt)
					if err != nil {
						return nil, fmt.Errorf("decode the filter salt: %w", err)
					}
					saltBytes = b
				}
				f.Hash = sensitive.HashExact(saltBytes, f.Value)
				f.Value = ""
				if f.Hash == "" {
					return nil, localui.Errorf(http.StatusBadRequest, nil,
						"filter %d: an exact match must be 1 to %d bytes long", i+1, sensitive.MaxExactBytes)
				}
			case byID[f.ID].Kind == sensitive.KindExact && byID[f.ID].Hash != "":
				f.Hash = byID[f.ID].Hash
			default:
				return nil, localui.Errorf(http.StatusBadRequest, nil,
					"filter %d: type the exact text to match", i+1)
			}
		}
		out = append(out, f)
	}
	if err := sensitive.ValidateList(out); err != nil {
		return nil, localui.Errorf(http.StatusBadRequest, err, "%s", filterMessage(err))
	}
	return out, nil
}

// filterMessage is a validation failure as the user reads it. The sensitive
// package's messages never quote a value, which is what makes them safe to
// show and to log.
func filterMessage(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "config: ")
	return strings.Replace(msg, "sensitive: invalid filter: ", "", 1)
}

// filterViews is the saved list as the editor shows it: an exact filter's
// secret was never kept, so there is none to show.
func filterViews(fs []sensitive.Filter) []localui.FilterView {
	out := make([]localui.FilterView, 0, len(fs))
	for _, f := range fs {
		v := localui.FilterView{
			ID:              f.ID,
			Kind:            string(f.Kind),
			Target:          string(f.Target),
			Value:           f.Value,
			CaseInsensitive: f.CaseInsensitive,
			Enabled:         f.Enabled,
			Label:           f.Label,
		}
		if f.Kind == sensitive.KindExact {
			v.Value = ""
		}
		out = append(out, v)
	}
	return out
}

// detectorViews lists every built-in detector with whether it is on.
func detectorViews(enabled []string) []localui.DetectorView {
	on := map[string]bool{}
	for _, n := range enabled {
		on[n] = true
	}
	ds := sensitive.Detectors()
	out := make([]localui.DetectorView, 0, len(ds))
	for _, d := range ds {
		out = append(out, localui.DetectorView{
			Name: d.Name, Title: d.Title, Description: d.Description, Enabled: on[d.Name],
		})
	}
	return out
}

// applySensitivePatch applies the sensitive-content part of a settings patch
// to next.
func applySensitivePatch(next *config.Settings, p localui.SettingsPatch) error {
	if p.DetectSensitive != nil {
		v := *p.DetectSensitive
		next.DetectSensitive = &v
	}
	if p.Detectors != nil {
		if err := checkDetectors(*p.Detectors); err != nil {
			return err
		}
		d := append([]string(nil), *p.Detectors...)
		next.Detectors = &d
	}
	if p.Filters != nil {
		if next.FilterSalt == "" && hasNewExact(*p.Filters) {
			salt, err := sensitive.NewSalt()
			if err != nil {
				return fmt.Errorf("create the filter salt: %w", err)
			}
			next.FilterSalt = salt
		}
		filters, err := resolveFilters(*p.Filters, next.Filters, next.FilterSalt)
		if err != nil {
			return err
		}
		next.Filters = filters
	}
	return nil
}

func hasNewExact(views []localui.FilterView) bool {
	for _, v := range views {
		if v.Kind == string(sensitive.KindExact) && v.Value != "" {
			return true
		}
	}
	return false
}

func (s *Service) currentClassifier() *sensitive.Classifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.classifier
}
