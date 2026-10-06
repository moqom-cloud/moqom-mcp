package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/moqom-cloud/moqom-go"
)

// Admin is the part of the moqom-go SDK the tools use.
//
// An interface so tests can drive the tools without a control plane; the
// production implementation is a thin pass-through to *moqom.Client.
type Admin interface {
	Rooms(ctx context.Context, query moqom.RoomQuery) (moqom.Page[moqom.Room], error)
	GetRoom(ctx context.Context, room string) (moqom.Room, error)
	CreateRoom(ctx context.Context, request moqom.NewRoom) (moqom.Room, error)
	CloseRoom(ctx context.Context, room string, code moqom.ModerationCode, actor moqom.Actor) (moqom.Room, error)
	MintToken(ctx context.Context, request moqom.TokenRequest) (moqom.Token, error)
	Participants(ctx context.Context, room string, query moqom.ParticipantQuery) (moqom.Page[moqom.Participant], error)
	Kick(ctx context.Context, room string, request moqom.KickRequest) (moqom.ModerationEntry, error)
	Ban(ctx context.Context, room string, request moqom.BanRequest) (moqom.ModerationEntry, error)
	Unban(ctx context.Context, room string, subject moqom.BanSubject, actor moqom.Actor) (moqom.ModerationEntry, error)
	ForceMute(ctx context.Context, room string, request moqom.MuteRequest) (moqom.ModerationEntry, error)
	Unmute(ctx context.Context, room string, request moqom.MuteRequest) (moqom.ModerationEntry, error)
}

// SDKAdmin implements Admin over moqom-go, dialling lazily.
//
// Lazily because the SDK needs the tenant at dial time and the key does not
// carry it: when MOQOM_TENANT is unset it is looked up from the billing API on
// first use, and a server that never touches rooms never makes that call.
type SDKAdmin struct {
	target   string
	insecure bool
	key      string
	tenant   string
	resolve  func(context.Context) (string, error)

	mu     sync.Mutex
	client *moqom.Client
}

// NewSDKAdmin prepares a lazily dialled client. resolveTenant is used when
// tenant is empty.
func NewSDKAdmin(target string, insecure bool, key, tenant string, resolveTenant func(context.Context) (string, error)) *SDKAdmin {
	return &SDKAdmin{target: target, insecure: insecure, key: key, tenant: tenant, resolve: resolveTenant}
}

func (a *SDKAdmin) sdk(ctx context.Context) (*moqom.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	tenant := a.tenant
	if tenant == "" {
		if a.resolve == nil {
			return nil, errors.New("MOQOM_TENANT is not set and cannot be looked up")
		}
		resolved, err := a.resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("looking up the tenant for this key (set MOQOM_TENANT to skip): %w", err)
		}
		if resolved == "" {
			return nil, errors.New("the billing API returned no tenant for this key; set MOQOM_TENANT")
		}
		tenant = resolved
	}
	options := []moqom.Option{
		moqom.WithCredential(a.key),
		moqom.WithTenant(tenant),
		moqom.WithActor(moqom.Actor{Reason: "via moqom-mcp"}),
	}
	if a.insecure {
		options = append(options, moqom.WithoutTransportSecurity())
	}
	client, err := moqom.Dial(a.target, options...)
	if err != nil {
		return nil, err
	}
	a.client = client
	return client, nil
}

// Close releases the connection if one was made.
func (a *SDKAdmin) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		return nil
	}
	err := a.client.Close()
	a.client = nil
	return err
}

func (a *SDKAdmin) Rooms(ctx context.Context, query moqom.RoomQuery) (moqom.Page[moqom.Room], error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.Page[moqom.Room]{}, err
	}
	return c.Rooms(ctx, query)
}

func (a *SDKAdmin) GetRoom(ctx context.Context, room string) (moqom.Room, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.Room{}, err
	}
	return c.Room(room).Get(ctx)
}

func (a *SDKAdmin) CreateRoom(ctx context.Context, request moqom.NewRoom) (moqom.Room, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.Room{}, err
	}
	return c.CreateRoom(ctx, request)
}

func (a *SDKAdmin) CloseRoom(ctx context.Context, room string, code moqom.ModerationCode, actor moqom.Actor) (moqom.Room, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.Room{}, err
	}
	return c.Room(room).Close(ctx, code, actor)
}

func (a *SDKAdmin) MintToken(ctx context.Context, request moqom.TokenRequest) (moqom.Token, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.Token{}, err
	}
	return c.MintToken(ctx, request)
}

func (a *SDKAdmin) Participants(ctx context.Context, room string, query moqom.ParticipantQuery) (moqom.Page[moqom.Participant], error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.Page[moqom.Participant]{}, err
	}
	return c.Room(room).Participants(ctx, query)
}

func (a *SDKAdmin) Kick(ctx context.Context, room string, request moqom.KickRequest) (moqom.ModerationEntry, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.ModerationEntry{}, err
	}
	return c.Room(room).Kick(ctx, request)
}

// Ban bans from one room, or tenant-wide when room is empty.
func (a *SDKAdmin) Ban(ctx context.Context, room string, request moqom.BanRequest) (moqom.ModerationEntry, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.ModerationEntry{}, err
	}
	if room == "" {
		return c.BanFromTenant(ctx, request)
	}
	return c.Room(room).Ban(ctx, request)
}

// Unban lifts a room ban, or a tenant-wide one when room is empty.
func (a *SDKAdmin) Unban(ctx context.Context, room string, subject moqom.BanSubject, actor moqom.Actor) (moqom.ModerationEntry, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.ModerationEntry{}, err
	}
	if room == "" {
		return c.UnbanFromTenant(ctx, subject, actor)
	}
	return c.Room(room).Unban(ctx, subject, actor)
}

func (a *SDKAdmin) ForceMute(ctx context.Context, room string, request moqom.MuteRequest) (moqom.ModerationEntry, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.ModerationEntry{}, err
	}
	return c.Room(room).ForceMute(ctx, request)
}

func (a *SDKAdmin) Unmute(ctx context.Context, room string, request moqom.MuteRequest) (moqom.ModerationEntry, error) {
	c, err := a.sdk(ctx)
	if err != nil {
		return moqom.ModerationEntry{}, err
	}
	return c.Room(room).Unmute(ctx, request)
}
