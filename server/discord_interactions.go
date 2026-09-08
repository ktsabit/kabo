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
	leaderboardCommandName           = "leaderboard"
	loserboardCommandName            = "loserboard"
	leaderboardSize                  = 10
	leaderboardImageFilename         = "leaderboard.png"
	leaderboardComponentPrefix       = "kabo:leaderboard:"
	loserboardComponentPrefix        = "kabo:loserboard:"
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
	Embeds          []discordEmbed         `json:"embeds"`
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
	Name string `json:"name"`
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
	board := interaction.Data.Name
	if board != leaderboardCommandName && board != loserboardCommandName {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("That command is not available."))
		return
	}
	if interaction.GuildID == "" {
		writeJSON(w, http.StatusOK, discordEphemeralResponse(fmt.Sprintf("Run `/%s` inside a Discord server where Kabo is installed.", board)))
		return
	}
	if s.results == nil {
		writeJSON(w, http.StatusOK, discordEphemeralResponse(fmt.Sprintf("The %s is temporarily unavailable.", board)))
		return
	}

	// A deferred response gives Discord the visible “thinking…” state while the
	// scoreboard card is rendered and uploaded as an attachment.
	writeJSON(w, http.StatusOK, discordDeferredResponse())
	go s.completeScoreboard(interaction, board)
}

func (s *server) handleDiscordComponent(w http.ResponseWriter, interaction discordInteraction) {
	if interaction.Data.CustomID == playActivityComponentID {
		writeJSON(w, http.StatusOK, discordInteractionResponse{Type: discordResponseLaunchActivity})
		return
	}
	board := leaderboardCommandName
	if strings.HasPrefix(interaction.Data.CustomID, loserboardComponentPrefix) {
		board = loserboardCommandName
	}
	ownerID, action, page, ok := parseLeaderboardComponentID(interaction.Data.CustomID)
	if !ok {
		writeJSON(w, http.StatusOK, discordEphemeralResponse("That control is no longer available."))
		return
	}
	viewerID, _ := discordInteractionViewer(interaction)
	if ownerID == "" || viewerID != ownerID {
		writeJSON(w, http.StatusOK, discordEphemeralResponse(fmt.Sprintf("Only the member who opened this %s can control it.", board)))
		return
	}

	writeJSON(w, http.StatusOK, discordInteractionResponse{Type: discordResponseDeferredUpdate})
	if action == "delete" {
		go func() {
			if err := s.deleteDiscordOriginal(interaction); err != nil {
				log.Printf("delete Discord %s for guild %s: %v", board, interaction.GuildID, err)
			}
		}()
		return
	}
	go s.completeScoreboardPage(interaction, page, ownerID, board)
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
	s.completeScoreboard(interaction, leaderboardCommandName)
}

func (s *server) completeLeaderboardPage(interaction discordInteraction, page int, ownerID string) {
	s.completeScoreboardPage(interaction, page, ownerID, leaderboardCommandName)
}

func (s *server) completeScoreboard(interaction discordInteraction, board string) {
	viewerID, _ := discordInteractionViewer(interaction)
	s.completeScoreboardPage(interaction, 0, viewerID, board)
}

func (s *server) completeScoreboardPage(interaction discordInteraction, page int, ownerID, board string) {
	if page < 0 {
		page = 0
	}
	entries, total, err := s.scoreboardPage(board, interaction.GuildID, page*leaderboardSize, leaderboardSize)
	if err != nil {
		log.Printf("read Discord %s for guild %s: %v", board, interaction.GuildID, err)
		if err := s.editDiscordOriginal(interaction, fmt.Sprintf("The %s is temporarily unavailable.", board)); err != nil {
			log.Printf("edit Discord %s error response: %v", board, err)
		}
		return
	}
	pageCount := leaderboardPageCount(total)
	if page >= pageCount {
		page = pageCount - 1
		entries, total, err = s.scoreboardPage(board, interaction.GuildID, page*leaderboardSize, leaderboardSize)
		if err != nil {
			log.Printf("read Discord %s page for guild %s: %v", board, interaction.GuildID, err)
			return
		}
	}

	viewerID, viewerName := discordInteractionViewer(interaction)
	if ownerID == "" {
		ownerID = viewerID
	}
	image, err := renderScoreboardPagePNG(entries, s.fetchLeaderboardAvatars(entries), page*leaderboardSize, ownerID, viewerName, board == loserboardCommandName)
	if err != nil {
		log.Printf("render Discord %s for guild %s: %v", board, interaction.GuildID, err)
		if err := s.editDiscordOriginal(interaction, "Could not render the scoreboard image. Please try again."); err != nil {
			log.Printf("edit Discord %s fallback: %v", board, err)
		}
		return
	}

	components := renderScoreboardComponents(board, ownerID, page, leaderboardPageCount(total))
	if err := s.editDiscordOriginalWithImage(interaction, image, components); err != nil {
		log.Printf("upload Discord %s for guild %s: %v", board, interaction.GuildID, err)
		if fallbackErr := s.editDiscordOriginal(interaction, "Could not upload the scoreboard image. Please try again."); fallbackErr != nil {
			log.Printf("edit Discord %s after upload failure: %v", board, fallbackErr)
		}
	}
}

func (s *server) scoreboardPage(board, guildID string, offset, limit int) ([]persistence.LeaderboardEntry, int, error) {
	if board == loserboardCommandName {
		return s.results.LoserboardPage(guildID, offset, limit)
	}
	return s.results.LeaderboardPage(guildID, offset, limit)
}

func renderLeaderboardEmbed(entries []persistence.LeaderboardEntry, imageURL string) discordEmbed {
	return renderScoreboardEmbed(entries, imageURL, false)
}

func renderScoreboardEmbed(_ []persistence.LeaderboardEntry, imageURL string, _ bool) discordEmbed {
	if imageURL == "" {
		return discordEmbed{}
	}
	return discordEmbed{Image: &discordEmbedImage{URL: imageURL}}
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
	return renderScoreboardComponents(leaderboardCommandName, ownerID, page, pageCount)
}

func renderScoreboardComponents(board, ownerID string, page, pageCount int) []discordComponent {
	prefix := componentPrefixForScoreboard(board)
	buttons := []discordComponent{{
		Type:     discordComponentButton,
		Style:    discordButtonPrimary,
		Label:    "Play Kabo",
		CustomID: playActivityComponentID,
	}}
	if pageCount > 1 {
		buttons = append(buttons,
			discordComponent{Type: discordComponentButton, Style: discordButtonSecondary, Label: "Previous", CustomID: scoreboardPageComponentID(board, ownerID, page-1), Disabled: page == 0},
			discordComponent{Type: discordComponentButton, Style: discordButtonSecondary, Label: fmt.Sprintf("%d / %d", page+1, pageCount), CustomID: prefix + ownerID + ":status", Disabled: true},
			discordComponent{Type: discordComponentButton, Style: discordButtonSecondary, Label: "Next", CustomID: scoreboardPageComponentID(board, ownerID, page+1), Disabled: page >= pageCount-1},
		)
	}
	buttons = append(buttons, discordComponent{
		Type:     discordComponentButton,
		Style:    discordButtonDanger,
		Emoji:    &discordEmoji{Name: "❌"},
		CustomID: prefix + ownerID + ":delete",
	})
	return []discordComponent{{Type: discordComponentActionRow, Components: buttons}}
}

func componentPrefixForScoreboard(board string) string {
	if board == loserboardCommandName {
		return loserboardComponentPrefix
	}
	return leaderboardComponentPrefix
}

func leaderboardPageComponentID(ownerID string, page int) string {
	return scoreboardPageComponentID(leaderboardCommandName, ownerID, page)
}

func scoreboardPageComponentID(board, ownerID string, page int) string {
	if page < 0 {
		page = 0
	}
	return fmt.Sprintf("%s%s:page:%d", componentPrefixForScoreboard(board), ownerID, page)
}

func parseLeaderboardComponentID(customID string) (ownerID, action string, page int, ok bool) {
	prefix := leaderboardComponentPrefix
	if strings.HasPrefix(customID, loserboardComponentPrefix) {
		prefix = loserboardComponentPrefix
	} else if !strings.HasPrefix(customID, prefix) {
		return "", "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(customID, prefix), ":")
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
	return renderScoreboardPageFallback(entries, 0, false)
}

func renderLeaderboardPageFallback(entries []persistence.LeaderboardEntry, rankOffset int) string {
	return renderScoreboardPageFallback(entries, rankOffset, false)
}

func renderScoreboardPageFallback(entries []persistence.LeaderboardEntry, rankOffset int, loserboard bool) string {
	if len(entries) == 0 {
		return "The table is open—finish a Discord Activity round in this server to place the first score."
	}
	var builder strings.Builder
	for index, entry := range entries {
		count, metric, rateLabel, rate := entry.Wins, "wins", "win rate", entry.WinRate
		if loserboard {
			count, metric, rateLabel, rate = entry.Losses, "losses", "loss rate", entry.LossRate
		}
		fmt.Fprintf(&builder, "%d. %s — %d %s · %.0f%% %s · %d times played\n", rankOffset+index+1, escapeDiscordText(entry.DisplayName), count, metric, rate, rateLabel, entry.Games)
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

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
		return fmt.Errorf("list Discord commands: Discord returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var registered []discordRegisteredCommand
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&registered)
	response.Body.Close()
	if decodeErr != nil {
		return decodeErr
	}
	registeredNames := make(map[string]bool, len(registered))
	for _, command := range registered {
		registeredNames[command.Name] = true
	}

	for _, command := range []discordCommandDefinition{
		{Name: leaderboardCommandName, Description: "Show this server's Kabo leaderboard", Type: 1},
		{Name: loserboardCommandName, Description: "Show this server's Kabo loserboard", Type: 1},
	} {
		if registeredNames[command.Name] {
			continue
		}
		body, err := json.Marshal(command)
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
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return fmt.Errorf("register /%s: Discord returned %s: %s", command.Name, resp.Status, strings.TrimSpace(string(responseBody)))
		}
		resp.Body.Close()
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

func (s *server) editDiscordOriginalWithImage(interaction discordInteraction, image []byte, components []discordComponent) error {
	content := ""
	payload := discordWebhookEdit{
		Content:    &content,
		Embeds:     []discordEmbed{},
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
