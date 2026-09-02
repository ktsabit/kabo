package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"kabo/server/persistence"
)

const (
	discordInteractionPing           = 1
	discordInteractionApplicationCmd = 2
	discordResponsePong              = 1
	discordResponseChannelMessage    = 4
	discordMessageFlagEphemeral      = 1 << 6
	maxDiscordInteractionBody        = 1 << 20
	leaderboardSize                  = 10
)

type discordInteraction struct {
	ID      string `json:"id"`
	Type    int    `json:"type"`
	Token   string `json:"token"`
	GuildID string `json:"guild_id"`
	Data    struct {
		Name string `json:"name"`
	} `json:"data"`
}

type discordInteractionResponse struct {
	Type int                    `json:"type"`
	Data discordMessageResponse `json:"data,omitempty"`
}

type discordMessageResponse struct {
	Content         string                 `json:"content,omitempty"`
	Flags           int                    `json:"flags,omitempty"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordCommandDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        int    `json:"type"`
}

func parseDiscordPublicKey(value string) ed25519.PublicKey {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		log.Printf("DISCORD_PUBLIC_KEY must be a %d-byte hex public key; Discord interactions are disabled", ed25519.PublicKeySize)
		return nil
	}
	return ed25519.PublicKey(raw)
}

func (s *server) handleDiscordInteraction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(s.interactionPublicKey) != ed25519.PublicKeySize {
		http.Error(w, "Discord interactions are not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDiscordInteractionBody))
	if err != nil {
		http.Error(w, "request body is too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	if !verifyDiscordRequest(
		s.interactionPublicKey,
		r.Header.Get("X-Signature-Ed25519"),
		r.Header.Get("X-Signature-Timestamp"),
		body,
	) {
		http.Error(w, "invalid request signature", http.StatusUnauthorized)
		return
	}

	var interaction discordInteraction
	if err := json.Unmarshal(body, &interaction); err != nil {
		http.Error(w, "invalid interaction", http.StatusBadRequest)
		return
	}

	switch interaction.Type {
	case discordInteractionPing:
		writeJSON(w, http.StatusOK, map[string]int{"type": discordResponsePong})
	case discordInteractionApplicationCmd:
		writeJSON(w, http.StatusOK, s.handleDiscordCommand(interaction))
	default:
		http.Error(w, "unsupported interaction type", http.StatusBadRequest)
	}
}

func (s *server) handleDiscordCommand(interaction discordInteraction) discordInteractionResponse {
	if interaction.Data.Name != "leaderboard" {
		return discordEphemeralResponse("That command is not available.")
	}
	if interaction.GuildID == "" {
		return discordEphemeralResponse("Run `/leaderboard` inside a Discord server where Kabo is installed.")
	}
	if s.results == nil {
		return discordEphemeralResponse("The leaderboard is temporarily unavailable.")
	}

	entries, err := s.results.Leaderboard(interaction.GuildID, leaderboardSize)
	if err != nil {
		log.Printf("read Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		return discordEphemeralResponse("The leaderboard is temporarily unavailable.")
	}

	return discordInteractionResponse{
		Type: discordResponseChannelMessage,
		Data: discordMessageResponse{
			Content:         renderLeaderboard(entries),
			AllowedMentions: discordAllowedMentions{Parse: []string{}},
		},
	}
}

func discordEphemeralResponse(content string) discordInteractionResponse {
	return discordInteractionResponse{
		Type: discordResponseChannelMessage,
		Data: discordMessageResponse{
			Content:         content,
			Flags:           discordMessageFlagEphemeral,
			AllowedMentions: discordAllowedMentions{Parse: []string{}},
		},
	}
}

func renderLeaderboard(entries []persistence.LeaderboardEntry) string {
	if len(entries) == 0 {
		return "**Kabo leaderboard**\nNo completed Kabo rounds have been recorded in this server yet."
	}

	var builder strings.Builder
	builder.WriteString("**Kabo leaderboard**\n")
	builder.WriteString("_Ranked by cumulative points; lower is better._\n\n")
	for index, entry := range entries {
		name := escapeDiscordText(entry.DisplayName)
		fmt.Fprintf(&builder, "%d. **%s** — %d pts · %d wins · %d games\n", index+1, name, entry.TotalScore, entry.Wins, entry.Games)
	}
	return strings.TrimSuffix(builder.String(), "\n")
}

func escapeDiscordText(value string) string {
	value = strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"~", "\\~",
		"|", "\\|",
		">", "\\>",
	).Replace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func verifyDiscordRequest(publicKey ed25519.PublicKey, signature, timestamp string, body []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize || timestamp == "" {
		return false
	}
	signatureBytes, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(signatureBytes) != ed25519.SignatureSize {
		return false
	}
	signed := make([]byte, 0, len(timestamp)+len(body))
	signed = append(signed, timestamp...)
	signed = append(signed, body...)
	return ed25519.Verify(publicKey, signed, signatureBytes)
}

func registerLeaderboardCommand(ctx context.Context, clientID, botToken, guildID string) error {
	if clientID == "" {
		return fmt.Errorf("DISCORD_CLIENT_ID is empty")
	}
	if botToken == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is empty")
	}

	endpoint := fmt.Sprintf(
		"https://discord.com/api/v10/applications/%s",
		url.PathEscape(clientID),
	)
	if guildID != "" {
		endpoint += "/guilds/" + url.PathEscape(guildID)
	}
	endpoint += "/commands"

	body, err := json.Marshal(discordCommandDefinition{
		Name:        "leaderboard",
		Description: "Show this server's Kabo leaderboard",
		Type:        1,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("Discord returned %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
