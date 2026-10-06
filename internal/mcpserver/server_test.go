package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moqom-cloud/moqom-go"

	"github.com/moqom-cloud/moqom-mcp/internal/billing"
	"github.com/moqom-cloud/moqom-mcp/internal/docs"
)

// fakeAdmin records calls and answers from memory.
type fakeAdmin struct {
	mu    sync.Mutex
	calls []string
	rooms map[string]moqom.Room
	bans  []moqom.BanRequest
	mints []moqom.TokenRequest
}

func newFakeAdmin() *fakeAdmin { return &fakeAdmin{rooms: map[string]moqom.Room{}} }

func (f *fakeAdmin) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeAdmin) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

func (f *fakeAdmin) Rooms(_ context.Context, _ moqom.RoomQuery) (moqom.Page[moqom.Room], error) {
	f.record("Rooms")
	f.mu.Lock()
	defer f.mu.Unlock()
	var page moqom.Page[moqom.Room]
	for _, room := range f.rooms {
		page.Items = append(page.Items, room)
	}
	return page, nil
}

func (f *fakeAdmin) GetRoom(_ context.Context, name string) (moqom.Room, error) {
	f.record("GetRoom")
	f.mu.Lock()
	defer f.mu.Unlock()
	room, ok := f.rooms[name]
	if !ok {
		return moqom.Room{}, moqom.ErrNotFound
	}
	return room, nil
}

func (f *fakeAdmin) CreateRoom(_ context.Context, request moqom.NewRoom) (moqom.Room, error) {
	f.record("CreateRoom")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rooms[request.Name]; ok {
		return moqom.Room{}, moqom.ErrExists
	}
	room := moqom.Room{
		Tenant: "acme", Name: request.Name, Namespace: "acme/" + request.Name, Policy: request.Policy,
		Lifecycle: moqom.LifecycleLive, CreatedAt: time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC),
	}
	f.rooms[request.Name] = room
	return room, nil
}

func (f *fakeAdmin) CloseRoom(_ context.Context, name string, _ moqom.ModerationCode, _ moqom.Actor) (moqom.Room, error) {
	f.record("CloseRoom")
	f.mu.Lock()
	defer f.mu.Unlock()
	room := f.rooms[name]
	room.Lifecycle = moqom.LifecycleEnded
	f.rooms[name] = room
	return room, nil
}

func (f *fakeAdmin) MintToken(_ context.Context, request moqom.TokenRequest) (moqom.Token, error) {
	f.record("MintToken")
	f.mu.Lock()
	f.mints = append(f.mints, request)
	f.mu.Unlock()
	return moqom.Token{
		Token: "eyJ.fake.token", ExpiresAt: time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC),
		Identity: moqom.Identity{Username: request.Username, Device: "dev-1"}, KeyID: "k1",
	}, nil
}

func (f *fakeAdmin) Participants(context.Context, string, moqom.ParticipantQuery) (moqom.Page[moqom.Participant], error) {
	f.record("Participants")
	return moqom.Page[moqom.Participant]{Items: []moqom.Participant{{
		Identity: moqom.Identity{Username: "carol", Device: "d1"},
		Grants:   moqom.Grants{Publish: moqom.PublishCamera},
		Presence: moqom.PresenceConnected,
	}}}, nil
}

func (f *fakeAdmin) Kick(_ context.Context, room string, request moqom.KickRequest) (moqom.ModerationEntry, error) {
	f.record("Kick")
	return moqom.ModerationEntry{ID: "m1", Room: room, Verb: moqom.VerbKick, Target: request.Target, Enforced: true}, nil
}

func (f *fakeAdmin) Ban(_ context.Context, room string, request moqom.BanRequest) (moqom.ModerationEntry, error) {
	f.record("Ban")
	f.mu.Lock()
	f.bans = append(f.bans, request)
	f.mu.Unlock()
	return moqom.ModerationEntry{ID: "m2", Room: room, Verb: moqom.VerbBan, BanSubject: request.Subject, Enforced: false}, nil
}

func (f *fakeAdmin) Unban(_ context.Context, room string, subject moqom.BanSubject, _ moqom.Actor) (moqom.ModerationEntry, error) {
	f.record("Unban")
	return moqom.ModerationEntry{ID: "m3", Room: room, Verb: moqom.VerbUnban, BanSubject: subject, Enforced: true}, nil
}

func (f *fakeAdmin) ForceMute(_ context.Context, room string, request moqom.MuteRequest) (moqom.ModerationEntry, error) {
	f.record("ForceMute")
	return moqom.ModerationEntry{ID: "m4", Room: room, Verb: moqom.VerbForceMute, Target: request.Target, MuteScope: request.Scope, Enforced: true}, nil
}

func (f *fakeAdmin) Unmute(_ context.Context, room string, request moqom.MuteRequest) (moqom.ModerationEntry, error) {
	f.record("Unmute")
	return moqom.ModerationEntry{ID: "m5", Room: room, Verb: moqom.VerbUnmute, Target: request.Target, Enforced: true}, nil
}

const currentKeyID = "0123456789abcdef"

type harness struct {
	session *mcp.ClientSession
	admin   *fakeAdmin
	revoked []string
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{admin: newFakeAdmin()}

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/billing", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"tenant":{"id":"acme","status":"active","auto_top_up":{"enabled":false,"below":{"micros":0,"display":"$0.00"},"amount":{"micros":0,"display":"$0.00"}},"created_at":"2026-01-01T00:00:00Z"},
			"balance":{"micros":42000000,"display":"$42.00"},"admitted":true,"recent":[]}`)
	})
	api.HandleFunc("GET /v1/rates", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"currency":"usd","model":"prepaid, cost-plus, capped","markup_basis_points":2500,
			"egress_cost_per_gib":{"micros":1,"display":"$0.00"},"platform_fee_per_gib":{"micros":1,"display":"$0.00"},
			"ceiling_per_1000_participant_minutes":{}}`)
	})
	api.HandleFunc("DELETE /v1/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		h.revoked = append(h.revoked, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("/llms-full.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "# Moderation\nSource: https://moqom.dev/guides/moderation/\n\n## Bans\n\nA device ban survives a new account.\n")
	})
	backend := httptest.NewServer(api)
	t.Cleanup(backend.Close)

	billingClient, err := billing.New(backend.URL, "mqk_"+currentKeyID+"_secret", billing.WithHTTPClient(backend.Client()))
	if err != nil {
		t.Fatal(err)
	}
	docsClient, err := docs.New(backend.URL, backend.Client())
	if err != nil {
		t.Fatal(err)
	}
	server := New(Deps{Admin: h.admin, Billing: billingClient, Docs: docsClient, CurrentKeyID: currentKeyID, Version: "test"})

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	h.session, err = client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.session.Close() })
	return h
}

func (h *harness) call(t *testing.T, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	result, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var structured map[string]any
	if result.StructuredContent != nil {
		raw, _ := json.Marshal(result.StructuredContent)
		_ = json.Unmarshal(raw, &structured)
	}
	return result, structured
}

func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestToolList(t *testing.T) {
	h := setup(t)
	result, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	want := []string{
		"list_rooms", "get_room", "create_room", "close_room",
		"mint_join_token",
		"kick_participant", "ban_participant", "unban", "force_mute", "unmute", "list_participants",
		"get_balance", "get_rates", "create_checkout_link", "set_auto_top_up", "open_billing_portal",
		"list_api_keys", "issue_api_key", "revoke_api_key",
		"search_docs", "get_doc_page",
	}
	for _, name := range want {
		if tools[name] == nil {
			t.Errorf("tool %q is missing", name)
		}
	}
	if len(tools) != len(want) {
		t.Errorf("got %d tools, want %d", len(tools), len(want))
	}

	for _, name := range []string{"close_room", "ban_participant", "revoke_api_key"} {
		tool := tools[name]
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s is not annotated destructive", name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required []string `json:"required"`
		}
		_ = json.Unmarshal(raw, &schema)
		if !slices.Contains(schema.Required, "confirm") {
			t.Errorf("%s does not require confirm: %s", name, raw)
		}
	}
	for _, name := range []string{"list_rooms", "get_balance", "search_docs", "list_api_keys"} {
		if tools[name].Annotations == nil || !tools[name].Annotations.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only", name)
		}
	}
	if !strings.Contains(tools["mint_join_token"].Description, "CREDENTIAL") {
		t.Error("mint_join_token does not warn that tokens are credentials")
	}
	if !strings.Contains(tools["issue_api_key"].Description, "ONCE") {
		t.Error("issue_api_key does not say the key is shown once")
	}
}

func TestRoomLifecycle(t *testing.T) {
	h := setup(t)

	result, room := h.call(t, "create_room", map[string]any{"room": "live/42", "owner": "jane", "recording": "allowed"})
	if result.IsError {
		t.Fatalf("create_room: %s", text(result))
	}
	if room["name"] != "live/42" || room["lifecycle"] != "live" {
		t.Errorf("room = %v", room)
	}

	result, _ = h.call(t, "create_room", map[string]any{"room": "live/42"})
	if !result.IsError || !strings.Contains(text(result), "already exists") {
		t.Errorf("duplicate create_room = %v %q", result.IsError, text(result))
	}

	result, _ = h.call(t, "create_room", map[string]any{"room": "live/43", "recording": "sometimes"})
	if !result.IsError {
		t.Error("create_room accepted a recording policy outside the enum")
	}

	result, list := h.call(t, "list_rooms", map[string]any{})
	if result.IsError || len(list["rooms"].([]any)) != 1 {
		t.Errorf("list_rooms = %v %q", list, text(result))
	}

	result, _ = h.call(t, "close_room", map[string]any{"room": "live/42", "confirm": false})
	if !result.IsError || !strings.Contains(text(result), "confirm") {
		t.Errorf("close_room without confirm = %v %q", result.IsError, text(result))
	}
	if h.admin.called("CloseRoom") {
		t.Fatal("close_room reached the SDK without confirm")
	}

	result, room = h.call(t, "close_room", map[string]any{"room": "live/42", "confirm": true})
	if result.IsError || room["lifecycle"] != "ended" {
		t.Errorf("close_room = %v %q", room, text(result))
	}
}

func TestMintJoinToken(t *testing.T) {
	h := setup(t)
	result, out := h.call(t, "mint_join_token", map[string]any{
		"username": "jane", "device_thumbprint": "thumb", "room": "live/42",
		"publish": []string{"audio", "video"}, "owner": true, "lifetime_seconds": 600,
	})
	if result.IsError {
		t.Fatalf("mint_join_token: %s", text(result))
	}
	if out["token"] != "eyJ.fake.token" || out["expires_at"] != "2026-10-06T11:00:00Z" || out["warning"] == "" {
		t.Errorf("out = %v", out)
	}
	request := h.admin.mints[0]
	if request.Publish != moqom.PublishAudio|moqom.PublishVideo || !request.Owner || request.Lifetime != 10*time.Minute || request.Preset != moqom.PresetNone {
		t.Errorf("request = %+v", request)
	}

	result, _ = h.call(t, "mint_join_token", map[string]any{"username": "jane", "device_thumbprint": "t", "publish": []string{"smell"}})
	if !result.IsError {
		t.Error("mint_join_token accepted an unknown publish scope")
	}
}

func TestBanNeedsConfirmAndReportsEnforcement(t *testing.T) {
	h := setup(t)
	result, _ := h.call(t, "ban_participant", map[string]any{"room": "live/42", "username": "mallory", "confirm": false})
	if !result.IsError || h.admin.called("Ban") {
		t.Fatalf("ban without confirm = %v %q", result.IsError, text(result))
	}

	result, entry := h.call(t, "ban_participant", map[string]any{
		"room": "live/42", "username": "mallory", "device": "d9", "duration_seconds": 3600, "confirm": true,
	})
	if result.IsError {
		t.Fatalf("ban: %s", text(result))
	}
	ban := h.admin.bans[0]
	if ban.Subject.DeviceOf.Username != "mallory" || ban.Subject.DeviceOf.Device != "d9" || ban.Duration != time.Hour {
		t.Errorf("ban = %+v", ban)
	}
	if entry["enforced"] != false || entry["note"] == nil {
		t.Errorf("unenforced ban not flagged: %v", entry)
	}

	result, _ = h.call(t, "ban_participant", map[string]any{"scope": "tenant", "room": "live/42", "username": "x", "confirm": true})
	if !result.IsError {
		t.Error("tenant-scope ban accepted a room")
	}
}

func TestBillingAndKeys(t *testing.T) {
	h := setup(t)
	result, summary := h.call(t, "get_balance", map[string]any{})
	if result.IsError {
		t.Fatalf("get_balance: %s", text(result))
	}
	if summary["admitted"] != true || summary["balance"].(map[string]any)["display"] != "$42.00" {
		t.Errorf("summary = %v", summary)
	}

	result, _ = h.call(t, "set_auto_top_up", map[string]any{"enabled": true, "below_cents": 500, "amount_cents": 2000})
	if !result.IsError || !strings.Contains(text(result), "confirm") {
		t.Errorf("enabling auto top-up without confirm = %v %q", result.IsError, text(result))
	}

	result, _ = h.call(t, "revoke_api_key", map[string]any{"id": currentKeyID, "confirm": true})
	if !result.IsError || len(h.revoked) != 0 {
		t.Errorf("revoking the server's own key = %v %q", result.IsError, text(result))
	}
	result, out := h.call(t, "revoke_api_key", map[string]any{"id": "fedcba9876543210", "confirm": true})
	if result.IsError || out["revoked"] != "fedcba9876543210" || len(h.revoked) != 1 {
		t.Errorf("revoke = %v %q", out, text(result))
	}
}

func TestSearchDocsTool(t *testing.T) {
	h := setup(t)
	result, out := h.call(t, "search_docs", map[string]any{"query": "device ban"})
	if result.IsError {
		t.Fatalf("search_docs: %s", text(result))
	}
	results := out["results"].([]any)
	if len(results) == 0 || results[0].(map[string]any)["url"] != "https://moqom.dev/guides/moderation/#bans" {
		t.Errorf("results = %v", results)
	}
}

func TestResourcesAndPrompt(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	resources, err := h.session.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uris []string
	for _, r := range resources.Resources {
		uris = append(uris, r.URI)
	}
	slices.Sort(uris)
	if strings.Join(uris, ",") != "moqom://billing/summary,moqom://rates" {
		t.Errorf("resources = %v", uris)
	}

	read, err := h.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moqom://rates"})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Contents) != 1 || !strings.Contains(read.Contents[0].Text, `"markup_basis_points": 2500`) {
		t.Errorf("rates = %+v", read.Contents)
	}

	prompt, err := h.session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "set-up-a-live-room",
		Arguments: map[string]string{"room": "live/launch", "host_username": "jane", "audience_url": "https://example.test/watch/{room}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := prompt.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"create_room", "mint_join_token", `"live/launch"`, "https://example.test/watch/{room}"} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt missing %q:\n%s", want, body)
		}
	}
}
