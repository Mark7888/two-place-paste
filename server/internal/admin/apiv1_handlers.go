package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/Mark7888/two-place-paste/server/internal/admin/apiv1"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// apiServer implements the generated API (apiv1.StrictServerInterface) on
// the admin operations the page uses too (ops.go). It only translates:
// request objects into operation arguments, views into response types. An
// operation's *opError is returned as the error, and writeAPIError turns it
// into a problem.
type apiServer struct{ s *Server }

var _ apiv1.StrictServerInterface = apiServer{}

// The relay: dashboard, health, sweep.

func (a apiServer) GetDashboard(ctx context.Context, _ apiv1.GetDashboardRequestObject) (apiv1.GetDashboardResponseObject, error) {
	v := a.s.dashboardView(ctx)
	out := apiv1.Dashboard{
		Groups:         v.GroupCount,
		Devices:        v.DeviceCount,
		UnusedTokens:   v.UnusedTokens,
		StaleGroups:    v.StaleCount,
		StaleAfterDays: v.StaleAfterDays,
		Problems:       v.Problems,
	}
	if st := v.Storage; st != nil {
		out.Storage = &apiv1.Storage{
			Backend:         st.Backend,
			Location:        st.Location,
			UsedBytes:       st.UsedBytes,
			LimitBytes:      st.LimitBytes,
			Entries:         st.Entries,
			GroupLimitBytes: st.GroupLimitBytes,
			EntryMaxBytes:   st.EntryMaxBytes,
			InlineMaxBytes:  st.InlineMaxBytes,
			Evictions:       st.Evictions,
			EvictedBytes:    st.EvictedBytes,
		}
		if u := st.Uploads; u != nil {
			out.Storage.Uploads = &apiv1.Uploads{Count: u.Count, ReservedBytes: u.ReservedBytes}
		}
		if sw := st.LastSweep; sw != nil {
			out.Storage.LastSweep = &apiv1.LastSweep{At: sw.At, TookMs: sw.TookMS, Error: strOrNil(sw.Error)}
		}
	}
	if st := v.Store; st != nil {
		out.Store = &apiv1.StoreTotals{
			UsedBytes:       st.UsedBytes,
			Items:           st.Items,
			GroupsOn:        st.GroupsOn,
			LimitBytes:      st.LimitBytes,
			GroupLimitBytes: st.GroupLimitBytes,
			MaxItemBytes:    st.MaxItemBytes,
			MaxGroupItems:   st.MaxGroupItems,
		}
	}
	return apiv1.GetDashboard200JSONResponse(out), nil
}

func (a apiServer) GetHealth(ctx context.Context, _ apiv1.GetHealthRequestObject) (apiv1.GetHealthResponseObject, error) {
	v := a.s.health(ctx, a.s.now())
	out := apiv1.Health{Version: v.Version, UptimeSeconds: v.UptimeSeconds, GoVersion: v.GoVersion}
	if rv := v.Redis; rv != nil {
		out.Redis = &apiv1.RedisHealth{
			Policy:      rv.Policy,
			PolicyState: apiv1.HealthState(rv.PolicyState),
			PolicyNote:  rv.PolicyNote,
			Aof:         rv.AOF,
			AofState:    apiv1.HealthState(rv.AOFState),
			AofNote:     rv.AOFNote,
			Memory:      rv.Memory,
			MaxMemory:   rv.MaxMemory,
		}
	}
	return apiv1.GetHealth200JSONResponse(out), nil
}

func (a apiServer) RequestSweep(ctx context.Context, _ apiv1.RequestSweepRequestObject) (apiv1.RequestSweepResponseObject, error) {
	queued, err := a.s.sweepNow(ctx)
	if err != nil {
		return nil, err
	}
	return apiv1.RequestSweep202JSONResponse{Queued: queued}, nil
}

// Groups.

func (a apiServer) ListGroups(ctx context.Context, req apiv1.ListGroupsRequestObject) (apiv1.ListGroupsResponseObject, error) {
	p := req.Params
	query := listQuery(p.Q, str(p.Sort), str(p.Dir), p.Page, num(p.Size))
	if p.Tag != nil {
		query.Set("tag", *p.Tag)
	}
	if p.Stale != nil && *p.Stale {
		query.Set("stale", "1")
	}
	v := a.s.groupListView(ctx, query)
	out := apiv1.GroupPage{
		Items: make([]apiv1.Group, 0, len(v.Rows)), Total: v.Total, Page: v.Page, Size: v.Size,
		Sort: v.Sort, Dir: apiv1.GroupPageDir(v.Dir), Problems: v.Problems,
	}
	for _, row := range v.Rows {
		out.Items = append(out.Items, apiGroup(row))
	}
	return apiv1.ListGroups200JSONResponse(out), nil
}

func (a apiServer) GetGroup(ctx context.Context, req apiv1.GetGroupRequestObject) (apiv1.GetGroupResponseObject, error) {
	g, err := a.s.findGroup(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return apiv1.GetGroup200JSONResponse(a.groupDetail(ctx, g)), nil
}

func (a apiServer) UpdateGroup(ctx context.Context, req apiv1.UpdateGroupRequestObject) (apiv1.UpdateGroupResponseObject, error) {
	if req.Body == nil {
		return nil, invalid("Send the fields to change.")
	}
	b := req.Body
	c := groupChange{Name: b.Name, Tag: b.Tag, Store: b.Store, Limit: limitChangeOf(b.Limit), StoreLimit: limitChangeOf(b.StoreLimit)}
	if c == (groupChange{}) {
		return nil, invalid("Send at least one field to change: name, tag, limit, store or store_limit.")
	}
	g, err := a.s.findGroup(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	if err := a.s.updateGroup(ctx, g, c); err != nil {
		return nil, err
	}
	// Read it back, so the answer shows what was stored.
	if g, err = a.s.findGroup(ctx, req.Handle); err != nil {
		return nil, err
	}
	return apiv1.UpdateGroup200JSONResponse(a.groupDetail(ctx, g)), nil
}

func (a apiServer) DeleteGroup(ctx context.Context, req apiv1.DeleteGroupRequestObject) (apiv1.DeleteGroupResponseObject, error) {
	if _, err := a.s.deleteGroupByHandle(ctx, req.Handle); err != nil {
		return nil, err
	}
	return apiv1.DeleteGroup204Response{}, nil
}

func (a apiServer) PurgeGroupEntries(ctx context.Context, req apiv1.PurgeGroupEntriesRequestObject) (apiv1.PurgeGroupEntriesResponseObject, error) {
	res, err := a.s.purgeGroup(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return apiv1.PurgeGroupEntries200JSONResponse{Entries: res.Entries, Bytes: res.Bytes}, nil
}

func (a apiServer) PurgeGroupStore(ctx context.Context, req apiv1.PurgeGroupStoreRequestObject) (apiv1.PurgeGroupStoreResponseObject, error) {
	before, err := a.s.purgeGroupStore(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return apiv1.PurgeGroupStore202JSONResponse{Items: before.Items, Bytes: before.Bytes}, nil
}

func (a apiServer) groupDetail(ctx context.Context, g store.Group) apiv1.GroupDetail {
	v := a.s.groupDetailView(ctx, g)
	out := apiv1.GroupDetail{
		Group:   apiGroup(v.Group),
		Devices: make([]apiv1.Device, 0, len(v.Devices)),
		Limit: apiv1.LimitSettings{
			Own:          apiv1.Limit{Enabled: v.OwnLimit.Enabled, Bytes: v.OwnLimit.Bytes},
			TagBytes:     v.TagLimitBytes,
			DefaultBytes: v.DefaultLimitBytes,
		},
		Problems: v.Problems,
	}
	// The page view has the store as its own section rather than as the
	// list's cell; it carries the same numbers.
	if st := v.Store; st != nil {
		out.Group.Store = &apiv1.GroupStore{
			Enabled: st.Enabled, Emptying: st.Emptying, UsedBytes: st.UsedBytes, UsedItems: st.UsedItems,
			LimitBytes: st.LimitBytes, LimitSource: apiv1.LimitSource(st.LimitSource),
		}
		out.StoreLimit = &apiv1.LimitSettings{
			Own:          apiv1.Limit{Enabled: st.OwnLimit.Enabled, Bytes: st.OwnLimit.Bytes},
			TagBytes:     st.TagLimitBytes,
			DefaultBytes: st.DefaultLimitBytes,
		}
	}
	for _, d := range v.Devices {
		out.Devices = append(out.Devices, apiv1.Device{
			ShortId: d.ShortID, Name: d.Name, CreatedAt: d.CreatedAt, LastSeen: d.LastSeen, Online: d.Online,
		})
	}
	return out
}

func apiGroup(g groupJSON) apiv1.Group {
	out := apiv1.Group{
		Handle:      g.Handle,
		ShortId:     g.ShortID,
		Name:        g.Name,
		Tag:         g.Tag,
		CreatedAt:   g.CreatedAt,
		DeviceCount: g.Devices,
		LastSeen:    g.LastSeen,
		Stale:       g.Stale,
		Entries:     g.Entries,
		OldestEntry: g.Oldest,
		Storage: apiv1.GroupStorage{
			LimitBytes:  g.LimitBytes,
			LimitSource: apiv1.LimitSource(g.LimitSource),
		},
	}
	if g.Usage != nil {
		out.Storage.UsedBytes = g.Usage.UsedBytes
		out.Storage.OverLimit = g.Usage.OverLimit
	}
	if st := g.Store; st != nil {
		out.Store = &apiv1.GroupStore{
			Enabled: st.Enabled, Emptying: st.Emptying, UsedBytes: st.UsedBytes, UsedItems: st.UsedItems,
			LimitBytes: st.LimitBytes, LimitSource: apiv1.LimitSource(st.LimitSource),
		}
	}
	return out
}

// Tags.

func (a apiServer) ListTags(ctx context.Context, req apiv1.ListTagsRequestObject) (apiv1.ListTagsResponseObject, error) {
	p := req.Params
	v, err := a.s.tagListView(ctx, listQuery(p.Q, str(p.Sort), str(p.Dir), p.Page, num(p.Size)))
	if err != nil {
		return nil, err
	}
	out := apiv1.TagPage{
		Items: make([]apiv1.Tag, 0, len(v.Rows)), Total: v.Total, Page: v.Page, Size: v.Size,
		Sort: v.Sort, Dir: apiv1.TagPageDir(v.Dir), Problems: v.Problems,
	}
	for _, row := range v.Rows {
		out.Items = append(out.Items, apiTag(row))
	}
	return apiv1.ListTags200JSONResponse(out), nil
}

func (a apiServer) CreateTag(ctx context.Context, req apiv1.CreateTagRequestObject) (apiv1.CreateTagResponseObject, error) {
	if req.Body == nil {
		return nil, invalid("Send the tag's name.")
	}
	name, err := a.s.createTag(ctx, req.Body.Name)
	if err != nil {
		return nil, err
	}
	row, err := a.s.tagView(ctx, name)
	if err != nil {
		return nil, err
	}
	return apiv1.CreateTag201JSONResponse(apiTag(row)), nil
}

func (a apiServer) GetTag(ctx context.Context, req apiv1.GetTagRequestObject) (apiv1.GetTagResponseObject, error) {
	row, err := a.s.tagView(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return apiv1.GetTag200JSONResponse(apiTag(row)), nil
}

func (a apiServer) UpdateTag(ctx context.Context, req apiv1.UpdateTagRequestObject) (apiv1.UpdateTagResponseObject, error) {
	if req.Body == nil {
		return nil, invalid("Send the limits to change.")
	}
	c := tagChange{Limit: limitChangeOf(req.Body.Limit), StoreLimit: limitChangeOf(req.Body.StoreLimit)}
	if c.Limit == nil && c.StoreLimit == nil {
		return nil, invalid("Send at least one of limit and store_limit.")
	}
	if err := a.s.updateTag(ctx, req.Name, c); err != nil {
		return nil, err
	}
	row, err := a.s.tagView(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return apiv1.UpdateTag200JSONResponse(apiTag(row)), nil
}

func (a apiServer) DeleteTag(ctx context.Context, req apiv1.DeleteTagRequestObject) (apiv1.DeleteTagResponseObject, error) {
	if err := a.s.deleteTag(ctx, req.Name); err != nil {
		return nil, err
	}
	return apiv1.DeleteTag204Response{}, nil
}

func apiTag(t tagJSON) apiv1.Tag {
	return apiv1.Tag{
		Name:          t.Name,
		CreatedAt:     timeOrNil(t.CreatedAt),
		Groups:        t.Groups,
		Devices:       t.Devices,
		UsedBytes:     t.UsedBytes,
		Limit:         apiv1.Limit{Enabled: t.Limit.Enabled, Bytes: t.Limit.Bytes},
		StoreLimit:    apiv1.Limit{Enabled: t.StoreLimit.Enabled, Bytes: t.StoreLimit.Bytes},
		StoreBytes:    t.StoreBytes,
		StoreGroupsOn: t.StoreGroupsOn,
	}
}

// Tokens.

func (a apiServer) ListTokens(ctx context.Context, req apiv1.ListTokensRequestObject) (apiv1.ListTokensResponseObject, error) {
	p := req.Params
	query := listQuery(p.Q, str(p.Sort), str(p.Dir), p.Page, num(p.Size))
	if p.Status != nil {
		query.Set("status", string(*p.Status))
	}
	v, err := a.s.tokenListView(ctx, query)
	if err != nil {
		return nil, err
	}
	out := apiv1.TokenPage{
		Items: make([]apiv1.Token, 0, len(v.Rows)), Total: v.Total, Page: v.Page, Size: v.Size,
		Sort: v.Sort, Dir: apiv1.TokenPageDir(v.Dir),
	}
	for _, row := range v.Rows {
		out.Items = append(out.Items, apiToken(row))
	}
	return apiv1.ListTokens200JSONResponse(out), nil
}

func (a apiServer) CreateToken(ctx context.Context, req apiv1.CreateTokenRequestObject) (apiv1.CreateTokenResponseObject, error) {
	if req.Body == nil {
		return nil, invalid("Send the token's name.")
	}
	b := req.Body
	in := tokenRequest{Name: b.Name, Note: deref(b.Note), Tag: deref(b.Tag)}
	if b.ExpiresInDays != nil {
		in.ExpiresDays = int(*b.ExpiresInDays)
		if in.ExpiresDays == 0 {
			// 0 would mean "never" to createToken; the API says that by
			// leaving the field out.
			return nil, invalid("Choose when the token expires: 1, 7 or 30 days, or leave expires_in_days out for never.")
		}
	}
	tok, err := a.s.createToken(ctx, in)
	if err != nil {
		return nil, err
	}
	return apiv1.CreateToken201JSONResponse{
		Token: apiToken(a.s.tokenView(tok, a.s.now())),
		Url:   a.s.creationURL(tok.Value),
	}, nil
}

func (a apiServer) GetToken(ctx context.Context, req apiv1.GetTokenRequestObject) (apiv1.GetTokenResponseObject, error) {
	tok, err := a.s.tokenByHandleOp(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return apiv1.GetToken200JSONResponse(apiToken(a.s.tokenView(tok, a.s.now()))), nil
}

func (a apiServer) RevokeToken(ctx context.Context, req apiv1.RevokeTokenRequestObject) (apiv1.RevokeTokenResponseObject, error) {
	if err := a.s.revokeToken(ctx, req.Handle); err != nil {
		return nil, err
	}
	return apiv1.RevokeToken204Response{}, nil
}

func (a apiServer) GetTokenLink(ctx context.Context, req apiv1.GetTokenLinkRequestObject) (apiv1.GetTokenLinkResponseObject, error) {
	tok, err := a.s.liveToken(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return apiv1.GetTokenLink200JSONResponse{Url: a.s.creationURL(tok.Value)}, nil
}

func (a apiServer) GetTokenQR(ctx context.Context, req apiv1.GetTokenQRRequestObject) (apiv1.GetTokenQRResponseObject, error) {
	png, err := a.s.tokenQR(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return apiv1.GetTokenQR200ImagepngResponse{Body: bytes.NewReader(png), ContentLength: int64(len(png))}, nil
}

func apiToken(t tokenJSON) apiv1.Token {
	out := apiv1.Token{
		Handle:    t.Handle,
		Name:      t.Name,
		Note:      t.Note,
		Tag:       t.Tag,
		CreatedAt: t.CreatedAt,
		Status:    apiv1.TokenStatus(t.Status),
		ExpiresAt: t.ExpiresAt,
	}
	if t.GroupHandle != "" {
		out.GroupHandle = &t.GroupHandle
	}
	return out
}

// Settings and the audit log.

func (a apiServer) GetSettings(ctx context.Context, _ apiv1.GetSettingsRequestObject) (apiv1.GetSettingsResponseObject, error) {
	return apiv1.GetSettings200JSONResponse(apiSettings(a.s.settingsView(a.s.settings(ctx)))), nil
}

func (a apiServer) UpdateSettings(ctx context.Context, req apiv1.UpdateSettingsRequestObject) (apiv1.UpdateSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, invalid("Send stale_after_days.")
	}
	st, err := a.s.saveSettings(ctx, float64(req.Body.StaleAfterDays))
	if err != nil {
		return nil, err
	}
	return apiv1.UpdateSettings200JSONResponse(apiSettings(a.s.settingsView(st))), nil
}

func apiSettings(v settingsJSON) apiv1.Settings {
	return apiv1.Settings{StaleAfterDays: v.StaleDays, DefaultStaleAfterDays: v.DefaultDays, MaxStaleAfterDays: v.MaxDays}
}

func (a apiServer) ListAudit(ctx context.Context, req apiv1.ListAuditRequestObject) (apiv1.ListAuditResponseObject, error) {
	p := req.Params
	query := listQuery(p.Q, str(p.Sort), str(p.Dir), p.Page, num(p.Size))
	if p.Action != nil {
		query.Set("action", *p.Action)
	}
	v, err := a.s.auditListView(ctx, query)
	if err != nil {
		return nil, err
	}
	out := apiv1.AuditPage{
		Items: make([]apiv1.AuditEntry, 0, len(v.Rows)), Total: v.Total, Page: v.Page, Size: v.Size,
		Sort: v.Sort, Dir: apiv1.AuditPageDir(v.Dir),
	}
	for _, e := range v.Rows {
		out.Items = append(out.Items, apiv1.AuditEntry{
			At: e.At, Action: e.Action, Label: e.Label, Target: e.Target, Detail: e.Detail, Ip: e.IP,
			Actor: apiActor(e),
		})
	}
	return apiv1.ListAudit200JSONResponse(out), nil
}

func apiActor(e auditJSON) apiv1.Actor {
	switch e.Actor {
	case actorAPIKey:
		return apiv1.Actor{Kind: apiv1.ApiKey, KeyId: strOrNil(e.ActorID), KeyName: strOrNil(e.ActorName)}
	case actorCLI:
		return apiv1.Actor{Kind: apiv1.Cli}
	default:
		return apiv1.Actor{Kind: apiv1.Admin}
	}
}

// The description. serveAPI answers both routes before the router sees
// them, with the committed bytes; these are what the generated router would
// serve without it.

func (a apiServer) GetOpenAPIYAML(context.Context, apiv1.GetOpenAPIYAMLRequestObject) (apiv1.GetOpenAPIYAMLResponseObject, error) {
	return apiv1.GetOpenAPIYAML200ApplicationyamlResponse{Body: bytes.NewReader(apiv1.SpecYAML), ContentLength: int64(len(apiv1.SpecYAML))}, nil
}

func (a apiServer) GetOpenAPIJSON(context.Context, apiv1.GetOpenAPIJSONRequestObject) (apiv1.GetOpenAPIJSONResponseObject, error) {
	var doc map[string]any
	if err := json.Unmarshal(apiv1.SpecJSON, &doc); err != nil {
		return nil, failed("The API description could not be read.")
	}
	return apiv1.GetOpenAPIJSON200JSONResponse(doc), nil
}

// Helpers.

// listQuery turns a list request's parameters into the query the list views
// take, so the API pages exactly like the admin page.
func listQuery(q *string, sort, dir string, page *int, size int) url.Values {
	v := url.Values{}
	if q != nil {
		v.Set("q", *q)
	}
	if sort != "" {
		v.Set("sort", sort)
	}
	if dir != "" {
		v.Set("dir", dir)
	}
	if page != nil {
		v.Set("page", strconv.Itoa(*page))
	}
	if size != 0 {
		v.Set("size", strconv.Itoa(size))
	}
	return v
}

func str[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func num[T ~int](p *T) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func limitChangeOf(c *apiv1.LimitChange) *limitChange {
	if c == nil {
		return nil
	}
	return &limitChange{Enabled: c.Enabled, Bytes: c.Bytes}
}
