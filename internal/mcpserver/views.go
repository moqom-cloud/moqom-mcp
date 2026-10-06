package mcpserver

import (
	"time"

	"github.com/moqom-cloud/moqom-go"
)

// The SDK's types carry no JSON tags, so tools return these instead: snake_case
// fields, RFC 3339 times, and empty values left out.

type identityView struct {
	Username string `json:"username"`
	Device   string `json:"device,omitempty"`
}

type policyView struct {
	MinimumAge          uint32 `json:"minimum_age,omitempty"`
	RequireVerification bool   `json:"require_age_verification,omitempty"`
	Recording           string `json:"recording,omitempty"`
	ResidencyZone       string `json:"residency_zone,omitempty"`
	MaxParticipants     uint32 `json:"max_participants,omitempty"`
	StageSlots          uint32 `json:"stage_slots,omitempty"`
	WatchdogLimit       uint32 `json:"watchdog_limit,omitempty"`
}

type roomView struct {
	Name              string        `json:"name"`
	Tenant            string        `json:"tenant,omitempty"`
	Namespace         string        `json:"namespace,omitempty"`
	Lifecycle         string        `json:"lifecycle,omitempty"`
	Policy            policyView    `json:"policy"`
	StartsAt          string        `json:"starts_at,omitempty"`
	CreatedAt         string        `json:"created_at,omitempty"`
	EndedAt           string        `json:"ended_at,omitempty"`
	ActingHost        *identityView `json:"acting_host,omitempty"`
	ParticipantCount  uint32        `json:"participant_count"`
	DistinctUserCount uint32        `json:"distinct_user_count"`
	IsRecording       bool          `json:"is_recording"`
}

type grantsView struct {
	Publish    string `json:"publish"`
	Moderation string `json:"moderation"`
	IsOwner    bool   `json:"is_owner,omitempty"`
	Role       string `json:"role"`
}

type participantView struct {
	Identity               identityView `json:"identity"`
	Grants                 grantsView   `json:"grants"`
	Presence               string       `json:"presence,omitempty"`
	SelfMuted              []string     `json:"self_muted,omitempty"`
	ForcedMutedForEveryone []string     `json:"forced_muted_for_everyone,omitempty"`
	ForcedMutedForAudience []string     `json:"forced_muted_for_audience,omitempty"`
	IsSpotlighted          bool         `json:"is_spotlighted,omitempty"`
	IsBackstage            bool         `json:"is_backstage,omitempty"`
	JoinedAt               string       `json:"joined_at,omitempty"`
	Locale                 string       `json:"locale,omitempty"`
}

type entryView struct {
	ID             string        `json:"id"`
	Room           string        `json:"room,omitempty"`
	At             string        `json:"at,omitempty"`
	Verb           string        `json:"verb"`
	ServiceAccount string        `json:"service_account,omitempty"`
	OnBehalfOf     string        `json:"on_behalf_of,omitempty"`
	Target         *identityView `json:"target,omitempty"`
	BanSubject     *banView      `json:"ban_subject,omitempty"`
	Code           string        `json:"code,omitempty"`
	Reason         string        `json:"reason,omitempty"`
	ExpiresAt      string        `json:"expires_at,omitempty"`
	Media          []string      `json:"media,omitempty"`
	MuteScope      string        `json:"mute_scope,omitempty"`
	BanScope       string        `json:"ban_scope,omitempty"`
	// Enforced is false while relays have not yet confirmed the directive.
	Enforced bool   `json:"enforced"`
	Note     string `json:"note,omitempty"`
}

type banView struct {
	Username string `json:"username,omitempty"`
	Device   string `json:"device,omitempty"`
	IPPrefix string `json:"ip_prefix,omitempty"`
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func viewRoom(r moqom.Room) roomView {
	view := roomView{
		Name: r.Name, Tenant: r.Tenant, Namespace: r.Namespace, Lifecycle: string(r.Lifecycle),
		Policy: policyView{
			MinimumAge:          r.Policy.AgeGate.MinimumAge,
			RequireVerification: r.Policy.AgeGate.RequireVerification,
			Recording:           string(r.Policy.Recording),
			ResidencyZone:       r.Policy.ResidencyZone,
			MaxParticipants:     r.Policy.MaxParticipants,
			StageSlots:          r.Policy.StageSlots,
			WatchdogLimit:       r.Policy.WatchdogLimit,
		},
		StartsAt: timeString(r.StartsAt), CreatedAt: timeString(r.CreatedAt), EndedAt: timeString(r.EndedAt),
		ParticipantCount: r.ParticipantCount, DistinctUserCount: r.DistinctUserCount, IsRecording: r.IsRecording,
	}
	if r.ActingHost.Username != "" {
		view.ActingHost = &identityView{Username: r.ActingHost.Username, Device: r.ActingHost.Device}
	}
	return view
}

func viewGrants(g moqom.Grants) grantsView {
	return grantsView{Publish: g.Publish.String(), Moderation: g.Moderation.String(), IsOwner: g.IsOwner, Role: string(g.Role())}
}

func mediaStrings(media []moqom.MediaKind) []string {
	if len(media) == 0 {
		return nil
	}
	out := make([]string, len(media))
	for i, m := range media {
		out[i] = string(m)
	}
	return out
}

func viewParticipant(p moqom.Participant) participantView {
	return participantView{
		Identity:               identityView{Username: p.Identity.Username, Device: p.Identity.Device},
		Grants:                 viewGrants(p.Grants),
		Presence:               string(p.Presence),
		SelfMuted:              mediaStrings(p.SelfMuted),
		ForcedMutedForEveryone: mediaStrings(p.ForcedMutedForEveryone),
		ForcedMutedForAudience: mediaStrings(p.ForcedMutedForAudience),
		IsSpotlighted:          p.IsSpotlighted,
		IsBackstage:            p.IsBackstage,
		JoinedAt:               timeString(p.JoinedAt),
		Locale:                 p.Locale,
	}
}

func viewEntry(e moqom.ModerationEntry) entryView {
	view := entryView{
		ID: e.ID, Room: e.Room, At: timeString(e.At), Verb: string(e.Verb),
		ServiceAccount: e.ServiceAccount, OnBehalfOf: e.OnBehalfOf,
		Code: string(e.Code), Reason: e.Reason, ExpiresAt: timeString(e.ExpiresAt),
		Media: mediaStrings(e.Media), MuteScope: string(e.MuteScope), BanScope: string(e.BanScope),
		Enforced: e.Enforced,
	}
	if e.Target.Username != "" {
		view.Target = &identityView{Username: e.Target.Username, Device: e.Target.Device}
	}
	s := e.BanSubject
	if s.Username != "" || s.DeviceOf.Username != "" || s.IPPrefix != "" {
		view.BanSubject = &banView{IPPrefix: s.IPPrefix, Username: s.Username}
		if s.DeviceOf.Username != "" {
			view.BanSubject.Username, view.BanSubject.Device = s.DeviceOf.Username, s.DeviceOf.Device
		}
	}
	if !e.Enforced {
		view.Note = "Recorded but not yet confirmed by every relay holding an affected session; enforcement is still catching up. Tell the user rather than reporting success outright."
	}
	return view
}
