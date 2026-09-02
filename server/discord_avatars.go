package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"kabo/server/persistence"
)

type discordUserProfile struct {
	Avatar *string `json:"avatar"`
}

func (s *server) fetchLeaderboardAvatars(entries []persistence.LeaderboardEntry) map[string]image.Image {
	if s.discord.BotToken == "" || len(entries) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	avatars := make(map[string]image.Image)
	var avatarsMu sync.Mutex
	var waitGroup sync.WaitGroup
	for _, entry := range entries {
		if entry.PlayerID == "" || !safeID.MatchString(entry.PlayerID) {
			continue
		}
		entry := entry
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			avatar, err := s.fetchDiscordAvatar(ctx, entry.PlayerID)
			if err != nil || avatar == nil {
				return
			}
			avatarsMu.Lock()
			avatars[entry.PlayerID] = avatar
			avatarsMu.Unlock()
		}()
	}
	waitGroup.Wait()
	return avatars
}

func (s *server) fetchDiscordAvatar(ctx context.Context, playerID string) (image.Image, error) {
	client := s.discord.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	profileEndpoint := fmt.Sprintf(
		"https://discord.com/api/v10/users/%s",
		url.PathEscape(playerID),
	)
	profileRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, profileEndpoint, nil)
	if err != nil {
		return nil, err
	}
	profileRequest.Header.Set("Authorization", "Bot "+s.discord.BotToken)
	profileResponse, err := client.Do(profileRequest)
	if err != nil {
		return nil, err
	}
	defer profileResponse.Body.Close()
	if profileResponse.StatusCode < 200 || profileResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Discord user lookup returned %s", profileResponse.Status)
	}
	var profile discordUserProfile
	if err := json.NewDecoder(io.LimitReader(profileResponse.Body, 16<<10)).Decode(&profile); err != nil {
		return nil, err
	}
	if profile.Avatar == nil || *profile.Avatar == "" {
		return nil, nil
	}

	avatarEndpoint := fmt.Sprintf(
		"https://cdn.discordapp.com/avatars/%s/%s.png?size=128",
		url.PathEscape(playerID),
		url.PathEscape(*profile.Avatar),
	)
	avatarRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, avatarEndpoint, nil)
	if err != nil {
		return nil, err
	}
	avatarResponse, err := client.Do(avatarRequest)
	if err != nil {
		return nil, err
	}
	defer avatarResponse.Body.Close()
	if avatarResponse.StatusCode < 200 || avatarResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Discord avatar lookup returned %s", avatarResponse.Status)
	}
	return png.Decode(io.LimitReader(avatarResponse.Body, 512<<10))
}
