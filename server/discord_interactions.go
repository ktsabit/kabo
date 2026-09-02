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
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"kabo/server/persistence"
)

const (
	discordInteractionPing           = 1
	discordInteractionApplicationCmd = 2
	discordResponsePong              = 1
	discordResponseChannelMessage    = 4
	discordResponseDeferred          = 5
	discordMessageFlagEphemeral      = 1 << 6
	maxDiscordInteractionBody        = 1 << 20
	leaderboardSize                  = 10
	leaderboardImageFilename         = "leaderboard.png"
)

type discordInteraction struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	Type          int    `json:"type"`
	Token         string `json:"token"`
	GuildID       string `json:"guild_id"`
	Guild         struct {
		Name string `json:"name"`
	} `json:"guild"`
	Data struct {
		Name string `json:"name"`
	} `json:"data"`
}

type discordInteractionResponse struct {
	Type int                     `json:"type"`
	Data *discordMessageResponse `json:"data,omitempty"`
}

type discordMessageResponse struct {
	Content         string                 `json:"content,omitempty"`
	Embeds          []discordEmbed         `json:"embeds,omitempty"`
	Flags           int                    `json:"flags,omitempty"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordEmbed struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Image       *discordEmbedImage  `json:"image,omitempty"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbedImage struct {
	URL string `json:"url"`
}

type discordEmbedFooter struct {
	Text string `json:"text"`
}

type discordWebhookEdit struct {
	Content         *string                `json:"content,omitempty"`
	Embeds          []discordEmbed         `json:"embeds,omitempty"`
	Attachments     []discordAttachment    `json:"attachments,omitempty"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

type discordAttachment struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
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
		s.handleDiscordCommand(w, interaction)
	default:
		http.Error(w, "unsupported interaction type", http.StatusBadRequest)
	}
}

func (s *server) handleDiscordCommand(w http.ResponseWriter, interaction discordInteraction) {
	if interaction.Data.Name != "leaderboard" {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("That command is not available."))
		return
	}
	if interaction.GuildID == "" {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("Run `/leaderboard` inside a Discord server where Kabo is installed."))
		return
	}
	if s.results == nil {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("The leaderboard is temporarily unavailable."))
		return
	}

	// A deferred response gives Discord the visible “thinking…” state while the
	// leaderboard card is rendered and uploaded as an attachment.
	writeJSON(w, http.StatusOK, discordDeferredResponse())
	go s.completeLeaderboard(interaction)
}

func discordEphemeralResponse(content string) discordInteractionResponse {
	return discordInteractionResponse{
		Type: discordResponseChannelMessage,
		Data: &discordMessageResponse{
			Content:         content,
			Flags:           discordMessageFlagEphemeral,
			AllowedMentions: discordAllowedMentions{Parse: []string{}},
		},
	}
}

func discordDeferredResponse() discordInteractionResponse {
	return discordInteractionResponse{Type: discordResponseDeferred}
}

func (s *server) completeLeaderboard(interaction discordInteraction) {
	entries, err := s.results.Leaderboard(interaction.GuildID, leaderboardSize)
	if err != nil {
		log.Printf("read Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		if err := s.editDiscordOriginal(interaction, "The leaderboard is temporarily unavailable."); err != nil {
			log.Printf("edit Discord leaderboard error response: %v", err)
		}
		return
	}

	serverName := interaction.Guild.Name
	if serverName == "" {
		serverName = "This server"
	}
	image, err := renderLeaderboardPNG(serverName, entries, s.fetchLeaderboardAvatars(entries))
	if err != nil {
		log.Printf("render Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		if err := s.editDiscordOriginal(interaction, renderLeaderboardFallback(entries)); err != nil {
			log.Printf("edit Discord leaderboard fallback: %v", err)
		}
		return
	}

	embed := renderLeaderboardEmbed(entries, "attachment://"+leaderboardImageFilename)
	if err := s.editDiscordOriginalWithImage(interaction, embed, image); err != nil {
		log.Printf("upload Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		if fallbackErr := s.editDiscordOriginal(interaction, renderLeaderboardFallback(entries)); fallbackErr != nil {
			log.Printf("edit Discord leaderboard after upload failure: %v", fallbackErr)
		}
	}
}

func renderLeaderboardEmbed(entries []persistence.LeaderboardEntry, imageURL string) discordEmbed {
	embed := discordEmbed{
		Title:       "Kabo · All-Time Standings",
		Description: "Ranked by total round wins · higher is better",
		Color:       0x3039B9,
		Footer:      &discordEmbedFooter{Text: "Kabo · win rounds, climb the table"},
	}
	if imageURL != "" {
		embed.Image = &discordEmbedImage{URL: imageURL}
	}
	if len(entries) == 0 {
		embed.Description = "The table is open—finish a Discord Activity round in this server to place the first score."
		return embed
	}
	if imageURL != "" {
		return embed
	}

	embed.Fields = make([]discordEmbedField, 0, len(entries))
	for index, entry := range entries {
		rank := fmt.Sprintf("#%d", index+1)
		switch index {
		case 0:
			rank = "🥇"
		case 1:
			rank = "🥈"
		case 2:
			rank = "🥉"
		}
		embed.Fields = append(embed.Fields, discordEmbedField{
			Name:   fmt.Sprintf("%s  %s", rank, escapeDiscordText(entry.DisplayName)),
			Value:  fmt.Sprintf("**%d wins** · %.0f%% win rate · %d rounds", entry.Wins, entry.WinRate, entry.Games),
			Inline: false,
		})
	}
	return embed
}

func renderLeaderboardFallback(entries []persistence.LeaderboardEntry) string {
	if len(entries) == 0 {
		return "The table is open—finish a Discord Activity round in this server to place the first score."
	}
	var builder strings.Builder
	for index, entry := range entries {
		fmt.Fprintf(&builder, "%d. %s — %d wins · %.0f%% win rate · %d rounds\n", index+1, escapeDiscordText(entry.DisplayName), entry.Wins, entry.WinRate, entry.Games)
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

func (s *server) editDiscordOriginal(interaction discordInteraction, content string) error {
	payload := discordWebhookEdit{
		Content:         &content,
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
	}
	return s.sendDiscordWebhookEdit(interaction, payload, nil)
}

func (s *server) editDiscordOriginalWithImage(interaction discordInteraction, embed discordEmbed, image []byte) error {
	payload := discordWebhookEdit{
		Embeds: []discordEmbed{embed},
		Attachments: []discordAttachment{{
			ID:       0,
			Filename: leaderboardImageFilename,
		}},
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	if err := multipartWriter.WriteField("payload_json", string(payloadJSON)); err != nil {
		return err
	}
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="files[0]"; filename="`+leaderboardImageFilename+`"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := multipartWriter.CreatePart(partHeader)
	if err != nil {
		return err
	}
	if _, err := part.Write(image); err != nil {
		return err
	}
	if err := multipartWriter.Close(); err != nil {
		return err
	}
	return s.sendDiscordWebhookEdit(interaction, payload, &multipartPayload{body: body.Bytes(), contentType: multipartWriter.FormDataContentType()})
}

type multipartPayload struct {
	body        []byte
	contentType string
}

func (s *server) sendDiscordWebhookEdit(interaction discordInteraction, payload discordWebhookEdit, multipartPayload *multipartPayload) error {
	applicationID := interaction.ApplicationID
	if applicationID == "" {
		applicationID = s.discord.ClientID
	}
	if applicationID == "" || interaction.Token == "" {
		return fmt.Errorf("interaction response credentials are incomplete")
	}

	var body io.Reader
	contentType := "application/json"
	if multipartPayload != nil {
		body = bytes.NewReader(multipartPayload.body)
		contentType = multipartPayload.contentType
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}

	endpoint := fmt.Sprintf(
		"https://discord.com/api/v10/webhooks/%s/%s/messages/@original",
		url.PathEscape(applicationID),
		url.PathEscape(interaction.Token),
	)
	req, err := http.NewRequest(http.MethodPatch, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	client := s.discord.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
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
