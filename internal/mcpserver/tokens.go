package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moqom-cloud/moqom-go"
)

var (
	publishScopes = strings2any("audio", "video", "screen_share", "data")
	presets       = strings2any("none", "moderator", "super_moderator", "owner")
)

type mintTokenIn struct {
	Username         string   `json:"username" jsonschema:"the customer's own identifier for the user this token speaks for"`
	DeviceThumbprint string   `json:"device_thumbprint" jsonschema:"JWK thumbprint (base64url SHA-256) of the joining device's public key, supplied by the client app"`
	Room             string   `json:"room,omitempty" jsonschema:"room to join; omit for a discovery-only token good for no room"`
	Publish          []string `json:"publish,omitempty" jsonschema:"what the holder may send; omit for a watch-only token"`
	Preset           string   `json:"preset,omitempty" jsonschema:"named moderation bundle; defaults to none (a viewer)"`
	Owner            bool     `json:"owner,omitempty" jsonschema:"make the holder the room's owner: immune to moderation and not demotable. Only for the real host"`
	Backstage        bool     `json:"backstage,omitempty" jsonschema:"join backstage rather than the live room"`
	AgeVerified      bool     `json:"age_verified,omitempty" jsonschema:"assert the tenant has verified this user's age, for age-gated rooms"`
	LifetimeSeconds  int64    `json:"lifetime_seconds,omitempty" jsonschema:"token lifetime; omit for the server default, keep it short"`
}

type mintTokenOut struct {
	Token     string       `json:"token"`
	ExpiresAt string       `json:"expires_at"`
	Identity  identityView `json:"identity"`
	KeyID     string       `json:"key_id,omitempty"`
	Warning   string       `json:"warning"`
}

func parsePublish(scopes []string) (moqom.PublishScope, error) {
	var publish moqom.PublishScope
	for _, scope := range scopes {
		switch scope {
		case "audio":
			publish |= moqom.PublishAudio
		case "video":
			publish |= moqom.PublishVideo
		case "screen_share":
			publish |= moqom.PublishScreenShare
		case "data":
			publish |= moqom.PublishData
		default:
			return 0, fmt.Errorf("unknown publish scope %q", scope)
		}
	}
	return publish, nil
}

func (t *tools) registerTokens(server *mcp.Server) {
	addTool(server, &mcp.Tool{
		Name: "mint_join_token",
		Description: "Mint a client join token for one user on one device, optionally for a room, with exactly the publish rights given. " +
			"The returned token is a CREDENTIAL: whoever holds it can join as that user until it expires. Hand it only to that user's client, " +
			"never post it in chat channels, tickets, or logs, and prefer short lifetimes. Use owner/preset=owner only for the actual host.",
		Annotations: titled(additive, "Mint join token"),
		InputSchema: schema[mintTokenIn](
			map[string][]any{"publish": publishScopes, "preset": presets},
			map[string]float64{"lifetime_seconds": 0}),
	}, func(ctx context.Context, in mintTokenIn) (mintTokenOut, error) {
		if err := requireText("username", in.Username); err != nil {
			return mintTokenOut{}, err
		}
		if err := requireText("device_thumbprint", in.DeviceThumbprint); err != nil {
			return mintTokenOut{}, err
		}
		publish, err := parsePublish(in.Publish)
		if err != nil {
			return mintTokenOut{}, err
		}
		preset := moqom.Preset(in.Preset)
		if preset == "" {
			preset = moqom.PresetNone
		}
		token, err := t.deps.Admin.MintToken(ctx, moqom.TokenRequest{
			Username:         in.Username,
			DeviceThumbprint: in.DeviceThumbprint,
			Room:             in.Room,
			Publish:          publish,
			Preset:           preset,
			Owner:            in.Owner,
			Backstage:        in.Backstage,
			AgeVerified:      in.AgeVerified,
			Lifetime:         time.Duration(in.LifetimeSeconds) * time.Second,
		})
		if err != nil {
			return mintTokenOut{}, err
		}
		return mintTokenOut{
			Token:     token.Token,
			ExpiresAt: timeString(token.ExpiresAt),
			Identity:  identityView{Username: token.Identity.Username, Device: token.Identity.Device},
			KeyID:     token.KeyID,
			Warning:   "This token is a bearer credential for " + in.Username + " until expires_at. Deliver it only to that user's client.",
		}, nil
	})
}
