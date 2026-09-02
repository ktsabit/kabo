package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kabo/server/auth"
	"kabo/server/game"
	"kabo/server/persistence"
)

func TestDiscordInteractionHandlerAcknowledgesPingAndServesLeaderboard(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persistence.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordRound(game.RoundResult{
		RoomID: "room", Platform: "discord", GuildID: "guild", Round: 1,
		StartedAt: time.Unix(10, 0), EndedAt: time.Unix(20, 0), EndReason: "called_end",
		Players: []game.PlayerResult{{ID: "player", Name: "Player", Score: 2, Winner: true}},
	}); err != nil {
		t.Fatal(err)
	}

	completed := make(chan struct{})
	var uploadedBody []byte
	var uploadedContentType string
	s := &server{
		results:              store,
		interactionPublicKey: publicKey,
		discord: auth.Discord{
			HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				uploadedContentType = request.Header.Get("Content-Type")
				uploadedBody, _ = io.ReadAll(request.Body)
				close(completed)
				return &http.Response{
					StatusCode: 200,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader("{}")),
					Header:     make(http.Header),
				}, nil
			})},
		},
	}

	ping := signedDiscordRequest(t, privateKey, `{"type":1}`)
	pingResponse := httptest.NewRecorder()
	s.handleDiscordInteraction(pingResponse, ping)
	if pingResponse.Code != 200 || strings.TrimSpace(pingResponse.Body.String()) != `{"type":1}` {
		t.Fatalf("PING response = %d %q", pingResponse.Code, pingResponse.Body.String())
	}

	command := signedDiscordRequest(t, privateKey, `{"type":2,"application_id":"app","token":"token","guild_id":"guild","guild":{"name":"Kabo"},"data":{"name":"leaderboard"}}`)
	commandResponse := httptest.NewRecorder()
	s.handleDiscordInteraction(commandResponse, command)
	if commandResponse.Code != 200 {
		t.Fatalf("command response status = %d, want 200", commandResponse.Code)
	}
	var response discordInteractionResponse
	if err := json.NewDecoder(commandResponse.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != discordResponseDeferred || response.Data != nil {
		t.Fatalf("command response = %+v, want deferred response", response)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("deferred leaderboard was not completed")
	}
	if !strings.HasPrefix(uploadedContentType, "multipart/form-data;") {
		t.Fatalf("leaderboard upload Content-Type = %q", uploadedContentType)
	}
	if !strings.Contains(string(uploadedBody), "attachment://leaderboard.png") || !strings.Contains(string(uploadedBody), "filename=\"leaderboard.png\"") || !bytes.Contains(uploadedBody, []byte("\x89PNG")) {
		t.Fatal("leaderboard upload did not contain the expected embed attachment")
	}
}

func TestDiscordInteractionHandlerRejectsInvalidSignature(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{interactionPublicKey: publicKey}
	request := httptest.NewRequest("POST", "/api/discord/interactions", bytes.NewReader([]byte(`{"type":1}`)))
	request.Header.Set("X-Signature-Ed25519", strings.Repeat("00", ed25519.SignatureSize))
	request.Header.Set("X-Signature-Timestamp", "1700000000")
	response := httptest.NewRecorder()

	s.handleDiscordInteraction(response, request)
	if response.Code != 401 {
		t.Fatalf("invalid signature status = %d, want 401", response.Code)
	}
}

func signedDiscordRequest(t *testing.T, privateKey ed25519.PrivateKey, body string) *http.Request {
	t.Helper()
	timestamp := "1700000000"
	signed := append([]byte(timestamp), []byte(body)...)
	signature := ed25519.Sign(privateKey, signed)
	request := httptest.NewRequest("POST", "/api/discord/interactions", strings.NewReader(body))
	request.Header.Set("X-Signature-Ed25519", hex.EncodeToString(signature))
	request.Header.Set("X-Signature-Timestamp", timestamp)
	return request
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
