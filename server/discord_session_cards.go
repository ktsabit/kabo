package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"time"

	"kabo/server/auth"
	"kabo/server/game"
	"kabo/server/transport"
)

const (
	sessionCardImageFilename            = "kabo-session.png"
	discordMessageSuppressNotifications = 1 << 12
)

type discordSessionCardManager struct {
	discord        auth.Discord
	updates        chan transport.RoomUpdate
	cards          map[string]discordSessionCard
	avatars        map[string]image.Image
	avatarAttempts map[string]bool
}

type discordSessionCard struct {
	ChannelID   string
	MessageID   string
	Fingerprint string
}

type discordBotMessagePayload struct {
	Content         string                 `json:"content,omitempty"`
	Embeds          []discordEmbed         `json:"embeds,omitempty"`
	Attachments     []discordAttachment    `json:"attachments,omitempty"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
	Components      []discordComponent     `json:"components,omitempty"`
	Flags           int                    `json:"flags,omitempty"`
	Nonce           string                 `json:"nonce,omitempty"`
	EnforceNonce    bool                   `json:"enforce_nonce,omitempty"`
}

type discordCreatedMessage struct {
	ID string `json:"id"`
}

func newDiscordSessionCardManager(discord auth.Discord) *discordSessionCardManager {
	manager := &discordSessionCardManager{
		discord:        discord,
		updates:        make(chan transport.RoomUpdate, 32),
		cards:          make(map[string]discordSessionCard),
		avatars:        make(map[string]image.Image),
		avatarAttempts: make(map[string]bool),
	}
	go manager.run()
	return manager
}

func (m *discordSessionCardManager) Enqueue(update transport.RoomUpdate) {
	if m == nil || update.Platform != "discord" || update.ChannelID == "" || m.discord.BotToken == "" {
		return
	}
	select {
	case m.updates <- update:
	default:
		select {
		case <-m.updates:
		default:
		}
		select {
		case m.updates <- update:
		default:
		}
	}
}

func (m *discordSessionCardManager) run() {
	for update := range m.updates {
		m.apply(update)
	}
}

func (m *discordSessionCardManager) apply(update transport.RoomUpdate) {
	key := update.InstanceID
	if key == "" {
		key = update.RoomID
	}
	fingerprint := sessionCardFingerprint(update)
	card := m.cards[key]
	if card.Fingerprint == fingerprint && card.ChannelID == update.ChannelID {
		return
	}

	m.fetchMissingAvatars(update)
	image, err := renderSessionCardPNG(update, m.avatars)
	if err != nil {
		log.Printf("render Discord session card for room %s: %v", update.RoomID, err)
		return
	}
	messageID, err := m.send(update, card.MessageID, image)
	if err != nil {
		// Remember the failed state so ordinary card draws do not hammer Discord.
		// The next join/leave or round transition changes the fingerprint and retries.
		card.Fingerprint = fingerprint
		card.ChannelID = update.ChannelID
		m.cards[key] = card
		log.Printf("publish Discord session card for room %s: %v", update.RoomID, err)
		return
	}
	m.cards[key] = discordSessionCard{
		ChannelID:   update.ChannelID,
		MessageID:   messageID,
		Fingerprint: fingerprint,
	}
}

func (m *discordSessionCardManager) fetchMissingAvatars(update transport.RoomUpdate) {
	if m.avatars == nil {
		m.avatars = make(map[string]image.Image)
	}
	if m.avatarAttempts == nil {
		m.avatarAttempts = make(map[string]bool)
	}
	missing := make([]string, 0, len(update.Players))
	for _, player := range update.Players {
		if player.ID == "" || m.avatarAttempts[player.ID] {
			continue
		}
		m.avatarAttempts[player.ID] = true
		missing = append(missing, player.ID)
	}
	for playerID, avatar := range fetchDiscordAvatars(m.discord, missing) {
		m.avatars[playerID] = avatar
	}
}

func (m *discordSessionCardManager) send(update transport.RoomUpdate, messageID string, image []byte) (string, error) {
	payload := discordBotMessagePayload{
		Content:         sessionCardMessageContent(update),
		Embeds:          []discordEmbed{{Image: &discordEmbedImage{URL: "attachment://" + sessionCardImageFilename}}},
		Attachments:     []discordAttachment{{ID: 0, Filename: sessionCardImageFilename}},
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
		Components: []discordComponent{{
			Type: discordComponentActionRow,
			Components: []discordComponent{{
				Type:     discordComponentButton,
				Style:    discordButtonPrimary,
				Label:    "Join Kabo",
				CustomID: playActivityComponentID,
			}},
		}},
	}
	method := http.MethodPost
	endpoint := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", url.PathEscape(update.ChannelID))
	if messageID == "" {
		payload.Flags = discordMessageSuppressNotifications
		payload.Nonce = sessionCardNonce(update)
		payload.EnforceNonce = true
	} else {
		method = http.MethodPatch
		endpoint += "/" + url.PathEscape(messageID)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	if err := multipartWriter.WriteField("payload_json", string(payloadJSON)); err != nil {
		return "", err
	}
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="files[0]"; filename="`+sessionCardImageFilename+`"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := multipartWriter.CreatePart(partHeader)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(image); err != nil {
		return "", err
	}
	if err := multipartWriter.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+m.discord.BotToken)
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	client := m.discord.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return "", fmt.Errorf("Discord returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var created discordCreatedMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("Discord returned a message without an ID")
	}
	return created.ID, nil
}

func sessionCardFingerprint(update transport.RoomUpdate) string {
	phase := "playing"
	if update.Phase == game.PhaseLobby {
		phase = "lobby"
	} else if update.Phase == game.PhaseEnded {
		phase = "ended"
	}
	parts := []string{update.ChannelID, itoa(update.Round), phase}
	for _, player := range update.Players {
		parts = append(parts, player.ID, cleanLeaderboardText(player.Name), fmt.Sprint(player.Connected))
	}
	winners := append([]string(nil), update.WinnerIDs...)
	sort.Strings(winners)
	parts = append(parts, winners...)
	return strings.Join(parts, "\x00")
}

func sessionCardNonce(update transport.RoomUpdate) string {
	value := update.InstanceID
	if value == "" {
		value = update.RoomID
	}
	hash := sha256.Sum256([]byte(value))
	return "kabo-" + hex.EncodeToString(hash[:8])
}

func sessionCardMessageContent(update transport.RoomUpdate) string {
	if update.Phase == game.PhaseEnded {
		if winners := sessionWinnerNames(update); winners != "" {
			return escapeDiscordText(winners) + " won round " + itoa(update.Round) + " of Kabo"
		}
	}
	names := make([]string, 0, len(update.Players))
	for _, player := range update.Players {
		if player.Connected {
			names = append(names, cleanLeaderboardText(player.Name))
		}
	}
	if len(names) == 0 {
		return "A Kabo table is open"
	}
	if update.Phase == game.PhaseLobby {
		return escapeDiscordText(names[0]) + " opened a Kabo table"
	}
	verb := " are "
	if len(names) == 1 {
		verb = " is "
	}
	return escapeDiscordText(humanNameList(names)) + verb + "playing Kabo"
}

func humanNameList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}
