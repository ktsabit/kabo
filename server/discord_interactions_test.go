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

	command := signedDiscordRequest(t, privateKey, `{"type":2,"application_id":"app","token":"token","guild_id":"guild","member":{"nick":"Full Server Nickname","user":{"id":"player","username":"account"}},"data":{"name":"leaderboard"}}`)
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
	if strings.Contains(string(uploadedBody), "All-Time Standings") || strings.Contains(string(uploadedBody), "Ranked by total") || strings.Contains(string(uploadedBody), "win rounds, climb") {
		t.Fatal("leaderboard upload still contains removed embed copy")
	}
	if !strings.Contains(string(uploadedBody), "kabo:leaderboard:player:delete") || !bytes.Contains(uploadedBody, []byte("❌")) {
		t.Fatal("leaderboard upload omitted its owner-scoped delete button")
	}
}

func TestLeaderboardComponentsProvideOwnerScopedPaginationAndDelete(t *testing.T) {
	rows := renderLeaderboardComponents("viewer", 1, 3)
	if len(rows) != 1 || len(rows[0].Components) != 5 {
		t.Fatalf("components = %+v, want one row with five buttons", rows)
	}
	buttons := rows[0].Components
	if buttons[0].Label != "Play Kabo" || buttons[0].Style != discordButtonPrimary || buttons[0].CustomID != playActivityComponentID {
		t.Fatalf("play button = %+v", buttons[0])
	}
	if buttons[1].Label != "Previous" || buttons[2].Label != "2 / 3" || buttons[3].Label != "Next" {
		t.Fatalf("pagination buttons = %+v", buttons[1:4])
	}
	if buttons[4].Style != discordButtonDanger || buttons[4].Emoji == nil || buttons[4].Emoji.Name != "❌" {
		t.Fatalf("delete button = %+v", buttons[4])
	}
	owner, action, page, ok := parseLeaderboardComponentID(buttons[3].CustomID)
	if !ok || owner != "viewer" || action != "page" || page != 2 {
		t.Fatalf("parsed next button = owner %q action %q page %d ok %v", owner, action, page, ok)
	}
	owner, action, _, ok = parseLeaderboardComponentID(buttons[4].CustomID)
	if !ok || owner != "viewer" || action != "delete" {
		t.Fatalf("parsed delete button = owner %q action %q ok %v", owner, action, ok)
	}
}

func TestDiscordPlayButtonLaunchesActivityForAnyMember(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{interactionPublicKey: publicKey}
	request := signedDiscordRequest(t, privateKey, `{"type":3,"member":{"user":{"id":"any-member"}},"data":{"custom_id":"kabo:activity:play"}}`)
	response := httptest.NewRecorder()
	s.handleDiscordInteraction(response, request)

	var interactionResponse discordInteractionResponse
	if err := json.NewDecoder(response.Body).Decode(&interactionResponse); err != nil {
		t.Fatal(err)
	}
	if interactionResponse.Type != discordResponseLaunchActivity {
		t.Fatalf("play response type = %d, want %d", interactionResponse.Type, discordResponseLaunchActivity)
	}
}

func TestDiscordEntryPointLaunchesActivity(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{interactionPublicKey: publicKey}
	request := signedDiscordRequest(t, privateKey, `{"type":2,"data":{"type":4,"name":"play"}}`)
	response := httptest.NewRecorder()
	s.handleDiscordInteraction(response, request)

	var interactionResponse discordInteractionResponse
	if err := json.NewDecoder(response.Body).Decode(&interactionResponse); err != nil {
		t.Fatal(err)
	}
	if interactionResponse.Type != discordResponseLaunchActivity {
		t.Fatalf("entry-point response type = %d, want %d", interactionResponse.Type, discordResponseLaunchActivity)
	}
}

func TestLeaderboardImageEmbedContainsNoExtraCopy(t *testing.T) {
	embed := renderLeaderboardEmbed([]persistence.LeaderboardEntry{{DisplayName: "Player"}}, "attachment://leaderboard.png")
	if embed.Title != "" || embed.Description != "" || embed.Footer != nil || len(embed.Fields) != 0 {
		t.Fatalf("image embed contains extra copy: %+v", embed)
	}
	if embed.Image == nil || embed.Image.URL != "attachment://leaderboard.png" {
		t.Fatalf("image embed = %+v", embed.Image)
	}
}

func TestDiscordLeaderboardDeleteButtonRemovesOriginalMessage(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan struct{})
	s := &server{
		interactionPublicKey: publicKey,
		discord: auth.Discord{HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodDelete {
				t.Errorf("delete control used %s, want DELETE", request.Method)
			}
			close(deleted)
			return &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})}},
	}
	request := signedDiscordRequest(t, privateKey, `{"type":3,"application_id":"app","token":"token","guild_id":"guild","member":{"user":{"id":"viewer"}},"data":{"custom_id":"kabo:leaderboard:viewer:delete"}}`)
	response := httptest.NewRecorder()
	s.handleDiscordInteraction(response, request)
	var interactionResponse discordInteractionResponse
	if err := json.NewDecoder(response.Body).Decode(&interactionResponse); err != nil {
		t.Fatal(err)
	}
	if interactionResponse.Type != discordResponseDeferredUpdate {
		t.Fatalf("delete response type = %d, want %d", interactionResponse.Type, discordResponseDeferredUpdate)
	}
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("delete control did not remove the original message")
	}
}

func TestDiscordLeaderboardControlsRejectOtherMembers(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{interactionPublicKey: publicKey}
	request := signedDiscordRequest(t, privateKey, `{"type":3,"member":{"user":{"id":"other"}},"data":{"custom_id":"kabo:leaderboard:owner:page:1"}}`)
	response := httptest.NewRecorder()
	s.handleDiscordInteraction(response, request)
	var interactionResponse discordInteractionResponse
	if err := json.NewDecoder(response.Body).Decode(&interactionResponse); err != nil {
		t.Fatal(err)
	}
	if interactionResponse.Type != discordResponseChannelMessage || interactionResponse.Data == nil || interactionResponse.Data.Flags != discordMessageFlagEphemeral {
		t.Fatalf("unauthorized control response = %+v", interactionResponse)
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
