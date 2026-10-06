package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moqom-cloud/moqom-go"
)

var (
	lifecycles = strings2any("scheduled", "live", "acting_host", "ended")
	codes      = strings2any("operator_action", "harassment", "adult_content", "violence", "self_harm",
		"copyright", "age_verification_required", "tenant_policy")
	recordings = strings2any("allowed", "required", "prohibited")
)

type listRoomsIn struct {
	Lifecycle []string `json:"lifecycle,omitempty" jsonschema:"only rooms in these phases; omit for all"`
	PageSize  uint32   `json:"page_size,omitempty" jsonschema:"rooms per page; omit for the server default"`
	PageToken string   `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

type listRoomsOut struct {
	Rooms         []roomView `json:"rooms"`
	NextPageToken string     `json:"next_page_token,omitempty"`
}

type roomIn struct {
	Room string `json:"room" jsonschema:"the room's name within the tenant, e.g. live/42"`
}

type createRoomIn struct {
	Room                   string `json:"room" jsonschema:"the room's name within the tenant, e.g. live/42; names come from the customer's own system"`
	Owner                  string `json:"owner,omitempty" jsonschema:"username of the initial owner; optional"`
	StartsAt               string `json:"starts_at,omitempty" jsonschema:"RFC 3339 time to create the room scheduled rather than live"`
	MinimumAge             uint32 `json:"minimum_age,omitempty" jsonschema:"age gate in years; 0 for none"`
	RequireAgeVerification bool   `json:"require_age_verification,omitempty" jsonschema:"require the tenant's own age verification rather than a self-declared age"`
	Recording              string `json:"recording,omitempty" jsonschema:"recording policy"`
	ResidencyZone          string `json:"residency_zone,omitempty" jsonschema:"where the room's data may live; immutable after creation"`
	MaxParticipants        uint32 `json:"max_participants,omitempty" jsonschema:"device cap; 0 for no limit"`
	StageSlots             uint32 `json:"stage_slots,omitempty" jsonschema:"how many may publish video at once; 0 for the server default (8)"`
	Reason                 string `json:"reason,omitempty" jsonschema:"why, recorded on the audit trail"`
	OnBehalfOf             string `json:"on_behalf_of,omitempty" jsonschema:"the human operator this is done for, recorded on the audit trail"`
}

type closeRoomIn struct {
	Room       string `json:"room" jsonschema:"the room to close"`
	Code       string `json:"code,omitempty" jsonschema:"reason code sent to every client; defaults to operator_action"`
	Reason     string `json:"reason,omitempty" jsonschema:"free-text reason recorded on the audit trail"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"the human operator this is done for"`
	Confirm    bool   `json:"confirm" jsonschema:"must be true; set only after the user explicitly approved closing this room"`
}

func (t *tools) registerRooms(server *mcp.Server) {
	addTool(server, &mcp.Tool{
		Name:        "list_rooms",
		Description: "List rooms in the MOQOM tenant, optionally filtered by lifecycle phase. Paginated: pass next_page_token back to continue.",
		Annotations: titled(readOnly, "List rooms"),
		InputSchema: schema[listRoomsIn](map[string][]any{"lifecycle": lifecycles}, nil),
	}, func(ctx context.Context, in listRoomsIn) (listRoomsOut, error) {
		query := moqom.RoomQuery{PageSize: in.PageSize, PageToken: in.PageToken}
		for _, l := range in.Lifecycle {
			query.Lifecycle = append(query.Lifecycle, moqom.Lifecycle(l))
		}
		page, err := t.deps.Admin.Rooms(ctx, query)
		if err != nil {
			return listRoomsOut{}, err
		}
		out := listRoomsOut{Rooms: make([]roomView, 0, len(page.Items)), NextPageToken: page.Next}
		for _, room := range page.Items {
			out.Rooms = append(out.Rooms, viewRoom(room))
		}
		return out, nil
	})

	addTool(server, &mcp.Tool{
		Name:        "get_room",
		Description: "Read one room: lifecycle, policy, participant counts, and whether it is recording.",
		Annotations: titled(readOnly, "Get room"),
	}, func(ctx context.Context, in roomIn) (roomView, error) {
		if err := requireText("room", in.Room); err != nil {
			return roomView{}, err
		}
		room, err := t.deps.Admin.GetRoom(ctx, in.Room)
		return viewRoom(room), err
	})

	addTool(server, &mcp.Tool{
		Name: "create_room",
		Description: "Create a room in the MOQOM tenant. Fails with 'already exists' rather than replacing an existing room. " +
			"Set starts_at to schedule it; otherwise it is live immediately. Residency cannot be changed later.",
		Annotations: titled(additive, "Create room"),
		InputSchema: schema[createRoomIn](map[string][]any{"recording": recordings}, nil),
	}, func(ctx context.Context, in createRoomIn) (roomView, error) {
		if err := requireText("room", in.Room); err != nil {
			return roomView{}, err
		}
		request := moqom.NewRoom{
			Name:  in.Room,
			Owner: in.Owner,
			Policy: moqom.Policy{
				AgeGate:         moqom.AgeGate{MinimumAge: in.MinimumAge, RequireVerification: in.RequireAgeVerification},
				Recording:       moqom.RecordingPolicy(in.Recording),
				ResidencyZone:   in.ResidencyZone,
				MaxParticipants: in.MaxParticipants,
				StageSlots:      in.StageSlots,
			},
			Actor: moqom.Actor{OnBehalfOf: in.OnBehalfOf, Reason: in.Reason},
		}
		if in.StartsAt != "" {
			startsAt, err := time.Parse(time.RFC3339, in.StartsAt)
			if err != nil {
				return roomView{}, fmt.Errorf("starts_at must be RFC 3339, e.g. 2026-10-06T18:00:00Z: %w", err)
			}
			request.StartsAt = startsAt
		}
		room, err := t.deps.Admin.CreateRoom(ctx, request)
		return viewRoom(room), err
	})

	addTool(server, &mcp.Tool{
		Name: "close_room",
		Description: "DESTRUCTIVE: end a room for everyone. Every participant is disconnected with the given reason code and the room cannot be reopened. " +
			"Requires confirm: true, which you may only set after the user explicitly approves closing this specific room.",
		Annotations: destructive("Close room"),
		InputSchema: schema[closeRoomIn](map[string][]any{"code": codes}, nil),
	}, func(ctx context.Context, in closeRoomIn) (roomView, error) {
		if !in.Confirm {
			return roomView{}, errNotConfirmed
		}
		if err := requireText("room", in.Room); err != nil {
			return roomView{}, err
		}
		code := moqom.ModerationCode(in.Code)
		if code == "" {
			code = moqom.CodeOperatorAction
		}
		room, err := t.deps.Admin.CloseRoom(ctx, in.Room, code, moqom.Actor{OnBehalfOf: in.OnBehalfOf, Reason: in.Reason})
		return viewRoom(room), err
	})
}
