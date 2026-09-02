package auth

import (
	"testing"
	"time"
)

func TestSessionMetadataRoundTripsWithIdentity(t *testing.T) {
	sessions := NewSessions()
	want := SessionMetadata{
		Platform:       "discord",
		ClientPlatform: "desktop",
		ApplicationID:  "app-123",
		InstanceID:     "instance-123",
		GuildID:        "guild-123",
		ChannelID:      "channel-123",
		LocationID:     "guild",
		CustomID:       "launch",
		ReferrerID:     "referrer",
	}
	id, err := sessions.CreateWithMetadata(Identity{ID: "user-123", Name: "Ada"}, "instance-123", want, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity, metadata, err := sessions.ResolveWithMetadata(id, "instance-123")
	if err != nil {
		t.Fatal(err)
	}
	if identity != (Identity{ID: "user-123", Name: "Ada"}) || metadata != want {
		t.Fatalf("session context changed during round trip: identity=%+v metadata=%+v", identity, metadata)
	}
}
