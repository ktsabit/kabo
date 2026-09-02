package main

import (
	"bytes"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"kabo/server/auth"
	"kabo/server/game"
	"kabo/server/transport"
)

func TestSessionCardCreatesOnceAndOnlyEditsForMeaningfulUpdates(t *testing.T) {
	requests := make([]string, 0, 3)
	bodies := make([][]byte, 0, 3)
	manager := &discordSessionCardManager{
		discord: auth.Discord{
			BotToken: "token",
			HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				requests = append(requests, request.Method+" "+request.URL.Path)
				bodies = append(bodies, body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"id":"message-1"}`)),
					Header:     make(http.Header),
				}, nil
			})},
		},
		cards:          make(map[string]discordSessionCard),
		avatarAttempts: map[string]bool{"kai": true, "waa": true},
	}
	update := transport.RoomUpdate{
		RoomID: "room", Platform: "discord", InstanceID: "instance", ChannelID: "channel",
		Phase:   game.PhaseLobby,
		Players: []transport.RoomPlayer{{ID: "kai", Name: "Kai", Connected: true}},
	}
	manager.apply(update)
	manager.apply(update)
	if len(requests) != 1 || requests[0] != "POST /api/v10/channels/channel/messages" {
		t.Fatalf("initial requests = %v, want one channel-message POST", requests)
	}
	if !bytes.Contains(bodies[0], []byte("Join Kabo")) || !bytes.Contains(bodies[0], []byte(sessionCardImageFilename)) || !bytes.Contains(bodies[0], []byte("\x89PNG")) {
		t.Fatal("created session card omitted its join button or PNG")
	}

	update.Phase = game.PhaseInitialPeek
	update.Round = 1
	manager.apply(update)
	update.Phase = game.PhaseAwaitDraw
	manager.apply(update)
	if len(requests) != 2 || requests[1] != "PATCH /api/v10/channels/channel/messages/message-1" {
		t.Fatalf("live-round requests = %v, want one in-place PATCH", requests)
	}

	update.Players = append(update.Players, transport.RoomPlayer{ID: "waa", Name: "Waa", Connected: true})
	manager.apply(update)
	if len(requests) != 3 || !strings.HasPrefix(requests[2], "PATCH ") {
		t.Fatalf("join requests = %v, want another in-place PATCH", requests)
	}
}

func TestSessionCardImageUsesCurrentRoundPlayersAndWinners(t *testing.T) {
	imageBytes, err := renderSessionCardPNG(transport.RoomUpdate{
		Round: 3,
		Phase: game.PhaseEnded,
		Players: []transport.RoomPlayer{
			{ID: "kai", Name: "Kai", Connected: true},
			{ID: "waa", Name: "Waa", Connected: true},
		},
		WinnerIDs: []string{"waa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != sessionCardImageWidth || decoded.Bounds().Dy() < 300 {
		t.Fatalf("session card dimensions = %v", decoded.Bounds())
	}
	if status, _ := sessionCardStatus(transport.RoomUpdate{
		Round: 3, Phase: game.PhaseEnded,
		Players:   []transport.RoomPlayer{{ID: "waa", Name: "Waa", Connected: true}},
		WinnerIDs: []string{"waa"},
	}, 1); status != "Waa won round 3" {
		t.Fatalf("completed-round status = %q", status)
	}
}

func TestSessionCardMessageUsesSingularAndPluralNames(t *testing.T) {
	one := transport.RoomUpdate{Phase: game.PhaseAwaitDraw, Players: []transport.RoomPlayer{{Name: "Kai", Connected: true}}}
	if got := sessionCardMessageContent(one); got != "Kai is playing Kabo" {
		t.Fatalf("one-player content = %q", got)
	}
	two := transport.RoomUpdate{Phase: game.PhaseAwaitDraw, Players: []transport.RoomPlayer{{Name: "Kai", Connected: true}, {Name: "Waa", Connected: true}}}
	if got := sessionCardMessageContent(two); got != "Kai and Waa are playing Kabo" {
		t.Fatalf("two-player content = %q", got)
	}
}
