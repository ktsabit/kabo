package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kabo/server/auth"
	"kabo/server/game"
	"kabo/server/persistence"
	"kabo/server/transport"

	"github.com/gorilla/websocket"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type server struct {
	rooms       *transport.Manager
	sessions    *auth.Sessions
	discord     auth.Discord
	allowGuests bool
	upgrader    websocket.Upgrader
}

func main() {
	port := env("PORT", "8080")
	results, err := persistence.Open(env("DB_PATH", "./kabo.sqlite"))
	if err != nil {
		log.Fatalf("open results database: %v", err)
	}
	defer results.Close()

	s := &server{
		rooms: transport.NewManager(transport.ManagerConfig{
			Timeouts: transport.TimeoutConfig{
				Initial: durationEnv("KABO_INITIAL_TIMEOUT", transport.DefaultInitialTimeout),
				Turn:    durationEnv("KABO_TURN_TIMEOUT", transport.DefaultTurnTimeout),
				Reveal:  durationEnv("KABO_REVEAL_TIMEOUT", transport.DefaultRevealTimeout),
			},
			Results: results,
		}),
		sessions: auth.NewSessions(),
		discord: auth.Discord{
			ClientID:     os.Getenv("DISCORD_CLIENT_ID"),
			ClientSecret: os.Getenv("DISCORD_CLIENT_SECRET"),
			BotToken:     os.Getenv("DISCORD_BOT_TOKEN"),
		},
		allowGuests: strings.EqualFold(env("ALLOW_GUESTS", "true"), "true"),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				return origin == "" ||
					sameOrigin(origin, r.Host) ||
					strings.HasSuffix(origin, ".discordsays.com")
			},
		},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, _ *http.Request) {
		if s.discord.ClientID == "" {
			http.Error(w, "Discord Application ID is not configured", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"clientId": s.discord.ClientID,
		})
	})

	mux.HandleFunc("/api/token", s.handleToken)
	mux.HandleFunc("/ws", s.handleWebSocket)

	clientDist := env("CLIENT_DIST", "../client/dist")
	if info, err := os.Stat(clientDist); err == nil && info.IsDir() {
		fs := http.FileServer(http.Dir(clientDist))
		mux.HandleFunc("/", spaHandler(clientDist, fs))
	}

	log.Printf("Kabo server listening on :%s", port)

	if err := http.ListenAndServe(":"+port, securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

func (s *server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Code       string `json:"code"`
		InstanceID string `json:"instanceId"`
		GuildID    string `json:"guildId"`
		ChannelID  string `json:"channelId"`
		LocationID string `json:"locationId"`
		Platform   string `json:"platform"`
		CustomID   string `json:"customId"`
		ReferrerID string `json:"referrerId"`
	}

	if err := json.NewDecoder(
		http.MaxBytesReader(w, r.Body, 8<<10),
	).Decode(&body); err != nil ||
		body.Code == "" ||
		!safeID.MatchString(body.InstanceID) ||
		(body.GuildID != "" && !safeID.MatchString(body.GuildID)) ||
		(body.ChannelID != "" && !safeID.MatchString(body.ChannelID)) ||
		(body.LocationID != "" && !safeID.MatchString(body.LocationID)) ||
		(body.Platform != "" && !safeID.MatchString(body.Platform)) ||
		(body.CustomID != "" && !safeID.MatchString(body.CustomID)) ||
		(body.ReferrerID != "" && !safeID.MatchString(body.ReferrerID)) {
		http.Error(w, "invalid token request", http.StatusBadRequest)
		return
	}

	token, identity, err := s.discord.Exchange(
		r.Context(),
		body.Code,
		body.GuildID,
	)
	if err == nil {
		err = s.discord.ValidateInstance(
			r.Context(),
			body.InstanceID,
			identity.ID,
		)
	}

	if err != nil {
		log.Printf("Discord authentication failed: %v", err)
		http.Error(w, "Discord authentication failed", http.StatusUnauthorized)
		return
	}

	lifetime := time.Hour
	if token.ExpiresIn > 0 {
		lifetime = time.Duration(token.ExpiresIn) * time.Second
	}

	// Identity is intentionally a value here; Sessions.CreateWithMetadata expects auth.Identity.
	platform := body.Platform
	if platform == "" {
		platform = "discord"
	}
	sessionID, err := s.sessions.CreateWithMetadata(*identity, body.InstanceID, auth.SessionMetadata{
		Platform:      platform,
		ApplicationID: s.discord.ClientID,
		InstanceID:    body.InstanceID,
		GuildID:       body.GuildID,
		ChannelID:     body.ChannelID,
		LocationID:    body.LocationID,
		CustomID:      body.CustomID,
		ReferrerID:    body.ReferrerID,
	}, lifetime)
	if err != nil {
		http.Error(w, "could not create game session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	_ = json.NewEncoder(w).Encode(map[string]string{
		"access_token": token.AccessToken,
		"session":      sessionID,
	})
}

func (s *server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room")
	if !safeID.MatchString(roomID) {
		http.Error(w, "invalid room", http.StatusBadRequest)
		return
	}

	var identity auth.Identity
	var err error
	roomMetadata := game.RoomMetadata{Platform: "browser", InstanceID: roomID}

	if sessionID := r.URL.Query().Get("session"); sessionID != "" {
		var sessionMetadata auth.SessionMetadata
		identity, sessionMetadata, err = s.sessions.ResolveWithMetadata(sessionID, roomID)
		roomMetadata = game.RoomMetadata{
			Platform:      sessionMetadata.Platform,
			ApplicationID: sessionMetadata.ApplicationID,
			InstanceID:    sessionMetadata.InstanceID,
			GuildID:       sessionMetadata.GuildID,
			ChannelID:     sessionMetadata.ChannelID,
			LocationID:    sessionMetadata.LocationID,
			CustomID:      sessionMetadata.CustomID,
			ReferrerID:    sessionMetadata.ReferrerID,
		}
	} else if s.allowGuests {
		identity = auth.Identity{
			ID:   r.URL.Query().Get("user"),
			Name: strings.TrimSpace(r.URL.Query().Get("name")),
		}

		if !safeID.MatchString(identity.ID) {
			err = errors.New("invalid guest identity")
		}

		if len(identity.Name) == 0 || len(identity.Name) > 40 {
			identity.Name = "Guest"
		}
	} else {
		err = errors.New("authentication required")
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	client, err := s.rooms.JoinWithMetadata(
		roomID,
		identity.ID,
		identity.Name,
		roomMetadata,
		conn,
	)
	if err != nil {
		_ = conn.WriteMessage(
			websocket.TextMessage,
			[]byte(fmt.Sprintf(
				`{"type":"error","code":"join_failed","message":%q}`,
				err.Error(),
			)),
		)
		_ = conn.Close()
		return
	}

	client.Run()
}

func spaHandler(root string, fs http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			path := filepath.Join(root, filepath.Clean(r.URL.Path))
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}

		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(origin, host string) bool {
	return origin == "http://"+host ||
		origin == "https://"+host ||
		(strings.HasPrefix(origin, "http://localhost:") &&
			strings.HasPrefix(host, "localhost:"))
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		log.Printf("invalid %s=%q; using %s", key, value, fallback)
		return fallback
	}
	return duration
}
