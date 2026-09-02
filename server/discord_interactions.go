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
	"strconv"
	"strings"
	"time"

	"kabo/server/persistence"
)

const (
	discordInteractionPing           = 1
	discordInteractionApplicationCmd = 2
	discordInteractionComponent      = 3
	discordResponsePong              = 1
	discordResponseChannelMessage    = 4
	discordResponseDeferred          = 5
	discordResponseDeferredUpdate    = 6
	discordResponseLaunchActivity    = 12
	discordMessageFlagEphemeral      = 1 << 6
	discordComponentActionRow        = 1
	discordComponentButton           = 2
	discordButtonPrimary             = 1
	discordButtonSecondary           = 2
	discordButtonDanger              = 4
	maxDiscordInteractionBody        = 1 << 20
	leaderboardSize                  = 10
	leaderboardImageFilename         = "leaderboard.png"
	leaderboardComponentPrefix       = "kabo:leaderboard:"
	playActivityComponentID          = "kabo:activity:play"
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
	Member struct {
		Nick string      `json:"nick"`
		User discordUser `json:"user"`
	} `json:"member"`
	Data struct {
		Type     int    `json:"type"`
		Name     string `json:"name"`
		CustomID string `json:"custom_id"`
	} `json:"data"`
}

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
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
	Components      []discordComponent     `json:"components,omitempty"`
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

type discordComponent struct {
	Type       int                `json:"type"`
	Style      int                `json:"style,omitempty"`
	Label      string             `json:"label,omitempty"`
	Emoji      *discordEmoji      `json:"emoji,omitempty"`
	CustomID   string             `json:"custom_id,omitempty"`
	Disabled   bool               `json:"disabled,omitempty"`
	Components []discordComponent `json:"components,omitempty"`
}

type discordEmoji struct {
	Name string `json:"name"`
}

type discordWebhookEdit struct {
	Content         *string                `json:"content,omitempty"`
	Embeds          []discordEmbed         `json:"embeds,omitempty"`
	Attachments     []discordAttachment    `json:"attachments,omitempty"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
	Components      []discordComponent     `json:"components,omitempty"`
}

type discordAttachment struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
}

type discordCommandDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        int    `json:"type"`
	Handler     int    `json:"handler,omitempty"`
}

type discordRegisteredCommand struct {
	ID   string `json:"id"`
	Type int    `json:"type"`
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
	case discordInteractionComponent:
		s.handleDiscordComponent(w, interaction)
	default:
		http.Error(w, "unsupported interaction type", http.StatusBadRequest)
	}
}

func (s *server) handleDiscordCommand(w http.ResponseWriter, interaction discordInteraction) {
	if interaction.Data.Type == 4 {
		writeJSON(w, http.StatusOK, discordInteractionResponse{Type: discordResponseLaunchActivity})
		return
	}
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

func (s *server) handleDiscordComponent(w http.ResponseWriter, interaction discordInteraction) {
	if interaction.Data.CustomID == playActivityComponentID {
		writeJSON(w, http.StatusOK, discordInteractionResponse{Type: discordResponseLaunchActivity})
		return
	}
	ownerID, action, page, ok := parseLeaderboardComponentID(interaction.Data.CustomID)
	if !ok {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("That control is no longer available."))
		return
	}
	viewerID, _ := discordInteractionViewer(interaction)
	if ownerID == "" || viewerID != ownerID {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("Only the member who opened this leaderboard can control it."))
		return
	}

	writeJSON(w, http.StatusOK, discordInteractionResponse{Type: discordResponseDeferredUpdate})
	if action == "delete" {
		go func() {
			if err := s.deleteDiscordOriginal(interaction); err != nil {
				log.Printf("delete Discord leaderboard for guild %s: %v", interaction.GuildID, err)
			}
		}()
		return
	}
	go s.completeLeaderboardPage(interaction, page, ownerID)
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
	viewerID, _ := discordInteractionViewer(interaction)
	s.completeLeaderboardPage(interaction, 0, viewerID)
}

func (s *server) completeLeaderboardPage(interaction discordInteraction, page int, ownerID string) {
	if page < 0 {
		page = 0
	}
	entries, total, err := s.results.LeaderboardPage(interaction.GuildID, page*leaderboardSize, leaderboardSize)
	if err != nil {
		log.Printf("read Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		if err := s.editDiscordOriginal(interaction, "The leaderboard is temporarily unavailable."); err != nil {
			log.Printf("edit Discord leaderboard error response: %v", err)
		}
		return
	}
	pageCount := leaderboardPageCount(total)
	if page >= pageCount {
		page = pageCount - 1
		entries, total, err = s.results.LeaderboardPage(interaction.GuildID, page*leaderboardSize, leaderboardSize)
		if err != nil {
			log.Printf("read Discord leaderboard page for guild %s: %v", interaction.GuildID, err)
			return
		}
	}

	viewerID, viewerName := discordInteractionViewer(interaction)
	if ownerID == "" {
		ownerID = viewerID
	}
	image, err := renderLeaderboardPagePNG(entries, s.fetchLeaderboardAvatars(entries), page*leaderboardSize, ownerID, viewerName)
	if err != nil {
		log.Printf("render Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		if err := s.editDiscordOriginal(interaction, renderLeaderboardPageFallback(entries, page*leaderboardSize)); err != nil {
			log.Printf("edit Discord leaderboard fallback: %v", err)
		}
		return
	}

	embed := renderLeaderboardEmbed(entries, "attachment://"+leaderboardImageFilename)
	components := renderLeaderboardComponents(ownerID, page, leaderboardPageCount(total))
	if err := s.editDiscordOriginalWithImage(interaction, embed, image, components); err != nil {
		log.Printf("upload Discord leaderboard for guild %s: %v", interaction.GuildID, err)
		if fallbackErr := s.editDiscordOriginal(interaction, renderLeaderboardPageFallback(entries, page*leaderboardSize)); fallbackErr != nil {
			log.Printf("edit Discord leaderboard after upload failure: %v", fallbackErr)
		}
	}
}

func renderLeaderboardEmbed(entries []persistence.LeaderboardEntry, imageURL string) discordEmbed {
	embed := discordEmbed{}
	if imageURL != "" {
		embed.Image = &discordEmbedImage{URL: imageURL}
		return embed
	}
	if len(entries) == 0 {
		embed.Description = "No completed rounds yet."
		return embed
	}
	embed.Title = "Kabo Leaderboard"

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

func discordInteractionViewer(interaction discordInteraction) (string, string) {
	name := strings.TrimSpace(interaction.Member.Nick)
	if name == "" {
		name = strings.TrimSpace(interaction.Member.User.GlobalName)
	}
	if name == "" {
		name = strings.TrimSpace(interaction.Member.User.Username)
	}
	return interaction.Member.User.ID, name
}

func leaderboardPageCount(total int) int {
	if total <= 0 {
		return 1
	}
	return (total + leaderboardSize - 1) / leaderboardSize
}

func renderLeaderboardComponents(ownerID string, page, pageCount int) []discordComponent {
	buttons := []discordComponent{{
		Type:     discordComponentButton,
		Style:    discordButtonPrimary,
		Label:    "Play Kabo",
		CustomID: playActivityComponentID,
	}}
	if pageCount > 1 {
		buttons = append(buttons,
			discordComponent{Type: discordComponentButton, Style: discordButtonSecondary, Label: "Previous", CustomID: leaderboardPageComponentID(ownerID, page-1), Disabled: page == 0},
			discordComponent{Type: discordComponentButton, Style: discordButtonSecondary, Label: fmt.Sprintf("%d / %d", page+1, pageCount), CustomID: leaderboardComponentPrefix + ownerID + ":status", Disabled: true},
			discordComponent{Type: discordComponentButton, Style: discordButtonSecondary, Label: "Next", CustomID: leaderboardPageComponentID(ownerID, page+1), Disabled: page >= pageCount-1},
		)
	}
	buttons = append(buttons, discordComponent{
		Type:     discordComponentButton,
		Style:    discordButtonDanger,
		Emoji:    &discordEmoji{Name: "❌"},
		CustomID: leaderboardComponentPrefix + ownerID + ":delete",
	})
	return []discordComponent{{Type: discordComponentActionRow, Components: buttons}}
}

func leaderboardPageComponentID(ownerID string, page int) string {
	if page < 0 {
		page = 0
	}
	return fmt.Sprintf("%s%s:page:%d", leaderboardComponentPrefix, ownerID, page)
}

func parseLeaderboardComponentID(customID string) (ownerID, action string, page int, ok bool) {
	if !strings.HasPrefix(customID, leaderboardComponentPrefix) {
		return "", "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(customID, leaderboardComponentPrefix), ":")
	if len(parts) == 2 && parts[0] != "" && parts[1] == "delete" {
		return parts[0], "delete", 0, true
	}
	if len(parts) != 3 || parts[0] == "" || parts[1] != "page" {
		return "", "", 0, false
	}
	parsedPage, err := strconv.Atoi(parts[2])
	if err != nil || parsedPage < 0 {
		return "", "", 0, false
	}
	return parts[0], "page", parsedPage, true
}

func renderLeaderboardFallback(entries []persistence.LeaderboardEntry) string {
	return renderLeaderboardPageFallback(entries, 0)
}

func renderLeaderboardPageFallback(entries []persistence.LeaderboardEntry, rankOffset int) string {
	if len(entries) == 0 {
		return "The table is open—finish a Discord Activity round in this server to place the first score."
	}
	var builder strings.Builder
	for index, entry := range entries {
		fmt.Fprintf(&builder, "%d. %s — %d wins · %.0f%% win rate · %d rounds\n", rankOffset+index+1, escapeDiscordText(entry.DisplayName), entry.Wins, entry.WinRate, entry.Games)
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

func configureDiscordEntryPoint(ctx context.Context, clientID, botToken string) error {
	if clientID == "" {
		return fmt.Errorf("DISCORD_CLIENT_ID is empty")
	}
	if botToken == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is empty")
	}
	commandsEndpoint := fmt.Sprintf(
		"https://discord.com/api/v10/applications/%s/commands",
		url.PathEscape(clientID),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, commandsEndpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bot "+botToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		response.Body.Close()
		return fmt.Errorf("Discord returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var commands []discordRegisteredCommand
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&commands)
	response.Body.Close()
	if decodeErr != nil {
		return decodeErr
	}

	method := http.MethodPost
	endpoint := commandsEndpoint
	for _, command := range commands {
		if command.Type == 4 {
			method = http.MethodPatch
			endpoint += "/" + url.PathEscape(command.ID)
			break
		}
	}
	body, err := json.Marshal(discordCommandDefinition{
		Name:        "play",
		Description: "Play Kabo",
		Type:        4,
		Handler:     1,
	})
	if err != nil {
		return err
	}
	request, err = http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bot "+botToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("Discord returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
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

func (s *server) editDiscordOriginalWithImage(interaction discordInteraction, embed discordEmbed, image []byte, components []discordComponent) error {
	payload := discordWebhookEdit{
		Embeds:     []discordEmbed{embed},
		Components: components,
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

func (s *server) deleteDiscordOriginal(interaction discordInteraction) error {
	applicationID := interaction.ApplicationID
	if applicationID == "" {
		applicationID = s.discord.ClientID
	}
	if applicationID == "" || interaction.Token == "" {
		return fmt.Errorf("interaction response credentials are incomplete")
	}
	endpoint := fmt.Sprintf(
		"https://discord.com/api/v10/webhooks/%s/%s/messages/@original",
		url.PathEscape(applicationID),
		url.PathEscape(interaction.Token),
	)
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
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
