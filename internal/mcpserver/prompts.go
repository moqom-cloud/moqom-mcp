package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "set-up-a-live-room",
		Title:       "Set up a live room",
		Description: "Walk through creating a room, minting the host's join token, and sharing an audience link.",
		Arguments: []*mcp.PromptArgument{
			{Name: "room", Description: "Room name, e.g. live/launch-party", Required: true},
			{Name: "host_username", Description: "The host's username in your own system", Required: true},
			{Name: "audience_url", Description: "Your app's watch-page URL pattern, e.g. https://example.com/watch/{room}"},
		},
	}, func(_ context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		args := request.Params.Arguments
		room := strings.TrimSpace(args["room"])
		host := strings.TrimSpace(args["host_username"])
		if room == "" || host == "" {
			return nil, fmt.Errorf("room and host_username are required")
		}
		audience := strings.TrimSpace(args["audience_url"])
		audienceStep := "4. Ask me for my app's watch-page URL. MOQOM does not host audience pages: the audience link is a page in my own app that " +
			"mints viewer tokens through my backend, so build it from my app's URL and the room name and give it to me to share."
		if audience != "" {
			audienceStep = fmt.Sprintf("4. Build the audience link by substituting the room name into %s (URL-encode it) and give it to me to share. "+
				"Viewers get their own watch-only tokens from my backend when they open it; do not mint tokens for them here.", audience)
		}
		text := fmt.Sprintf(`Help me set up a live MOQOM room.

1. Call get_balance and check "admitted" is true. If it is false, explain why and offer create_checkout_link before going further.
2. Call create_room with room %[1]q and owner %[2]q. If it already exists, call get_room and confirm with me before reusing it.
3. Mint the host's join token with mint_join_token: username %[2]q, room %[1]q, publish ["audio","video","screen_share"], owner true. You need the host device's thumbprint; ask me for it rather than inventing one. Treat the token as a password: give it only to the host's client and remind me it expires at expires_at.
%[3]s
5. Summarise what you created, with the room's lifecycle and the token's expiry, and remind me close_room ends the room for everyone when we are done.`,
			room, host, audienceStep)
		return &mcp.GetPromptResult{
			Description: "Set up live room " + room,
			Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
		}, nil
	})
}
