package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moqom-cloud/moqom-go"
)

var (
	mediaKinds = strings2any("audio", "video", "screen_share")
	muteScopes = strings2any("everyone", "audience")
	banScopes  = strings2any("room", "tenant")
)

type listParticipantsIn struct {
	Room             string `json:"room" jsonschema:"the room"`
	IncludeBackstage bool   `json:"include_backstage,omitempty" jsonschema:"include participants who are backstage"`
	IncludeDeparted  bool   `json:"include_departed,omitempty" jsonschema:"include participants who left but are still within retention"`
	PageSize         uint32 `json:"page_size,omitempty" jsonschema:"participants per page"`
	PageToken        string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

type listParticipantsOut struct {
	Participants  []participantView `json:"participants"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}

type kickIn struct {
	Room       string `json:"room" jsonschema:"the room"`
	Username   string `json:"username" jsonschema:"who to remove"`
	Device     string `json:"device,omitempty" jsonschema:"one device only; omit to remove every device of that user"`
	Code       string `json:"code,omitempty" jsonschema:"reason code shown to the user; defaults to operator_action"`
	Reason     string `json:"reason,omitempty" jsonschema:"free-text reason for the audit trail"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"the human moderator this is done for"`
}

type banIn struct {
	Scope           string `json:"scope,omitempty" jsonschema:"room (default) bans from one room; tenant bans from every room"`
	Room            string `json:"room,omitempty" jsonschema:"the room; required when scope is room, must be empty when scope is tenant"`
	Username        string `json:"username,omitempty" jsonschema:"ban this account (with device: ban only that device of the account)"`
	Device          string `json:"device,omitempty" jsonschema:"ban one device of username, which survives the user making a new account"`
	IPPrefix        string `json:"ip_prefix,omitempty" jsonschema:"ban an address or CIDR prefix instead; blunt, catches households and campuses"`
	DurationSeconds int64  `json:"duration_seconds,omitempty" jsonschema:"how long; omit or 0 for permanent"`
	Code            string `json:"code,omitempty" jsonschema:"reason code shown to the user; defaults to operator_action"`
	Reason          string `json:"reason,omitempty" jsonschema:"free-text reason for the audit trail and appeals"`
	OnBehalfOf      string `json:"on_behalf_of,omitempty" jsonschema:"the human moderator this is done for"`
	Confirm         bool   `json:"confirm" jsonschema:"must be true; set only after the user explicitly approved this ban"`
}

type unbanIn struct {
	Scope      string `json:"scope,omitempty" jsonschema:"room (default) or tenant; must match the ban being lifted"`
	Room       string `json:"room,omitempty" jsonschema:"the room; required when scope is room"`
	Username   string `json:"username,omitempty" jsonschema:"account the ban was keyed on"`
	Device     string `json:"device,omitempty" jsonschema:"device, when the ban was keyed on one device"`
	IPPrefix   string `json:"ip_prefix,omitempty" jsonschema:"address or prefix, when the ban was keyed on one"`
	Reason     string `json:"reason,omitempty" jsonschema:"why, for the audit trail"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"the human moderator this is done for"`
}

type muteIn struct {
	Room            string   `json:"room" jsonschema:"the room"`
	Username        string   `json:"username" jsonschema:"who to mute"`
	Device          string   `json:"device,omitempty" jsonschema:"one device only; omit for every device of the user"`
	Scope           string   `json:"scope" jsonschema:"everyone stops it for all including other hosts; audience stops it for viewers only"`
	Media           []string `json:"media,omitempty" jsonschema:"which media; omit for all"`
	DurationSeconds int64    `json:"duration_seconds,omitempty" jsonschema:"how long; omit or 0 to hold until lifted with unmute (force_mute only)"`
	Reason          string   `json:"reason,omitempty" jsonschema:"free-text reason for the audit trail"`
	OnBehalfOf      string   `json:"on_behalf_of,omitempty" jsonschema:"the human moderator this is done for"`
}

func codeOrDefault(code string) moqom.ModerationCode {
	if code == "" {
		return moqom.CodeOperatorAction
	}
	return moqom.ModerationCode(code)
}

func banTarget(scope, room, username, device, ipPrefix string) (string, moqom.BanSubject, error) {
	switch scope {
	case "", "room":
		if room == "" {
			return "", moqom.BanSubject{}, errors.New("room is required when scope is room")
		}
	case "tenant":
		if room != "" {
			return "", moqom.BanSubject{}, errors.New("leave room empty when scope is tenant")
		}
	default:
		return "", moqom.BanSubject{}, fmt.Errorf("unknown scope %q", scope)
	}
	switch {
	case ipPrefix != "" && (username != "" || device != ""):
		return "", moqom.BanSubject{}, errors.New("give either username (and optionally device) or ip_prefix, not both")
	case ipPrefix != "":
		return room, moqom.BanAddress(ipPrefix), nil
	case username != "" && device != "":
		return room, moqom.BanDevice(moqom.Identity{Username: username, Device: device}), nil
	case username != "":
		return room, moqom.BanAccount(username), nil
	case device != "":
		return "", moqom.BanSubject{}, errors.New("a device ban needs the username the device belongs to")
	default:
		return "", moqom.BanSubject{}, errors.New("give a username or an ip_prefix")
	}
}

func muteRequest(in muteIn) (moqom.MuteRequest, error) {
	if err := requireText("room", in.Room); err != nil {
		return moqom.MuteRequest{}, err
	}
	if err := requireText("username", in.Username); err != nil {
		return moqom.MuteRequest{}, err
	}
	if in.Scope != "everyone" && in.Scope != "audience" {
		return moqom.MuteRequest{}, errors.New(`scope must be "everyone" or "audience"`)
	}
	request := moqom.MuteRequest{
		Target:   moqom.ParticipantRef{Username: in.Username, Device: in.Device},
		Scope:    moqom.MuteScope(in.Scope),
		Duration: time.Duration(in.DurationSeconds) * time.Second,
		Actor:    moqom.Actor{OnBehalfOf: in.OnBehalfOf, Reason: in.Reason},
	}
	for _, m := range in.Media {
		request.Media = append(request.Media, moqom.MediaKind(m))
	}
	return request, nil
}

func (t *tools) registerModeration(server *mcp.Server) {
	addTool(server, &mcp.Tool{
		Name:        "list_participants",
		Description: "List who is in a room: identity, role and grants, presence, and mute state per device.",
		Annotations: titled(readOnly, "List participants"),
	}, func(ctx context.Context, in listParticipantsIn) (listParticipantsOut, error) {
		if err := requireText("room", in.Room); err != nil {
			return listParticipantsOut{}, err
		}
		page, err := t.deps.Admin.Participants(ctx, in.Room, moqom.ParticipantQuery{
			IncludeBackstage: in.IncludeBackstage, IncludeDeparted: in.IncludeDeparted,
			PageSize: in.PageSize, PageToken: in.PageToken,
		})
		if err != nil {
			return listParticipantsOut{}, err
		}
		out := listParticipantsOut{Participants: make([]participantView, 0, len(page.Items)), NextPageToken: page.Next}
		for _, p := range page.Items {
			out.Participants = append(out.Participants, viewParticipant(p))
		}
		return out, nil
	})

	addTool(server, &mcp.Tool{
		Name: "kick_participant",
		Description: "Remove a participant from a room now. They may rejoin immediately: this is for a bad moment, not a bad actor (use ban_participant for that). " +
			"Check 'enforced' in the result.",
		Annotations: &mcp.ToolAnnotations{Title: "Kick participant", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		InputSchema: schema[kickIn](map[string][]any{"code": codes}, nil),
	}, func(ctx context.Context, in kickIn) (entryView, error) {
		if err := requireText("room", in.Room); err != nil {
			return entryView{}, err
		}
		if err := requireText("username", in.Username); err != nil {
			return entryView{}, err
		}
		entry, err := t.deps.Admin.Kick(ctx, in.Room, moqom.KickRequest{
			Target: moqom.ParticipantRef{Username: in.Username, Device: in.Device},
			Code:   codeOrDefault(in.Code),
			Actor:  moqom.Actor{OnBehalfOf: in.OnBehalfOf, Reason: in.Reason},
		})
		return viewEntry(entry), err
	})

	addTool(server, &mcp.Tool{
		Name: "ban_participant",
		Description: "DESTRUCTIVE: ban an account, one device, or an IP prefix from a room (scope=room) or from every room in the tenant (scope=tenant), " +
			"ending their current sessions. Permanent unless duration_seconds is set. Requires confirm: true, which you may only set after the user " +
			"explicitly approves this ban. Prefer username or device over ip_prefix, which catches bystanders.",
		Annotations: destructive("Ban participant"),
		InputSchema: schema[banIn](map[string][]any{"code": codes, "scope": banScopes}, map[string]float64{"duration_seconds": 0}),
	}, func(ctx context.Context, in banIn) (entryView, error) {
		if !in.Confirm {
			return entryView{}, errNotConfirmed
		}
		room, subject, err := banTarget(in.Scope, in.Room, in.Username, in.Device, in.IPPrefix)
		if err != nil {
			return entryView{}, err
		}
		entry, err := t.deps.Admin.Ban(ctx, room, moqom.BanRequest{
			Subject:  subject,
			Duration: time.Duration(in.DurationSeconds) * time.Second,
			Code:     codeOrDefault(in.Code),
			Actor:    moqom.Actor{OnBehalfOf: in.OnBehalfOf, Reason: in.Reason},
		})
		return viewEntry(entry), err
	})

	addTool(server, &mcp.Tool{
		Name:        "unban",
		Description: "Lift a ban before it expires. The scope and subject (username, username+device, or ip_prefix) must match the ban being lifted.",
		Annotations: &mcp.ToolAnnotations{Title: "Unban", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		InputSchema: schema[unbanIn](map[string][]any{"scope": banScopes}, nil),
	}, func(ctx context.Context, in unbanIn) (entryView, error) {
		room, subject, err := banTarget(in.Scope, in.Room, in.Username, in.Device, in.IPPrefix)
		if err != nil {
			return entryView{}, err
		}
		entry, err := t.deps.Admin.Unban(ctx, room, subject, moqom.Actor{OnBehalfOf: in.OnBehalfOf, Reason: in.Reason})
		return viewEntry(entry), err
	})

	muteSchema := func() *jsonschema.Schema {
		return schema[muteIn](map[string][]any{"scope": muteScopes, "media": mediaKinds}, map[string]float64{"duration_seconds": 0})
	}
	addTool(server, &mcp.Tool{
		Name: "force_mute",
		Description: "Stop a participant's media without their client's cooperation. scope=everyone is relay-enforced for all, including other hosts; " +
			"scope=audience hides it from viewers while hosts still hear them. Lift with unmute using the same scope.",
		Annotations: &mcp.ToolAnnotations{Title: "Force mute", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		InputSchema: muteSchema(),
	}, func(ctx context.Context, in muteIn) (entryView, error) {
		request, err := muteRequest(in)
		if err != nil {
			return entryView{}, err
		}
		entry, err := t.deps.Admin.ForceMute(ctx, in.Room, request)
		return viewEntry(entry), err
	})

	addTool(server, &mcp.Tool{
		Name:        "unmute",
		Description: "Lift a forced mute. scope must match the mute being lifted: lifting an audience mute does nothing to an everyone mute.",
		Annotations: &mcp.ToolAnnotations{Title: "Unmute", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		InputSchema: muteSchema(),
	}, func(ctx context.Context, in muteIn) (entryView, error) {
		request, err := muteRequest(in)
		if err != nil {
			return entryView{}, err
		}
		request.Duration = 0
		entry, err := t.deps.Admin.Unmute(ctx, in.Room, request)
		return viewEntry(entry), err
	})
}
