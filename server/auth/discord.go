package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Discord struct {
	ClientID     string
	ClientSecret string
	BotToken     string
	HTTPClient   *http.Client
}

type TokenResult struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type discordUser struct {
	ID         string  `json:"id"`
	Username   string  `json:"username"`
	GlobalName *string `json:"global_name"`
}

type discordGuildMember struct {
	Nick   *string `json:"nick"`
	Avatar *string `json:"avatar"`
}

type activityInstance struct {
	ApplicationID string   `json:"application_id"`
	InstanceID    string   `json:"instance_id"`
	Users         []string `json:"users"`
}

func (d Discord) Exchange(
	ctx context.Context,
	code string,
	guildID string,
) (*TokenResult, *Identity, error) {
	if d.ClientID == "" || d.ClientSecret == "" {
		return &TokenResult{}, &Identity{}, errors.New("Discord OAuth is not configured")
	}

	form := url.Values{
		"client_id":     {d.ClientID},
		"client_secret": {d.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://discord.com/api/oauth2/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return &TokenResult{}, &Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var token TokenResult
	if err := d.doJSON(req, &token); err != nil {
		return &TokenResult{}, &Identity{}, fmt.Errorf("token exchange: %w", err)
	}

	req, err = http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://discord.com/api/v10/users/@me",
		nil,
	)
	if err != nil {
		return &TokenResult{}, &Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	var user discordUser
	if err := d.doJSON(req, &user); err != nil {
		return &TokenResult{}, &Identity{}, fmt.Errorf("fetch Discord user: %w", err)
	}

	// Default name resolution:
	// Discord username -> global display name -> guild/server nickname.
	name := user.Username
	if user.GlobalName != nil && *user.GlobalName != "" {
		name = *user.GlobalName
	}

	if guildID != "" {
		member, err := d.fetchGuildMember(ctx, token.AccessToken, guildID)
		if err != nil {
			return &TokenResult{}, &Identity{}, fmt.Errorf("fetch Discord guild member: %w", err)
		}

		if member.Nick != nil && *member.Nick != "" {
			name = *member.Nick
		}
	}

	return &token, &Identity{
		ID:   user.ID,
		Name: name,
	}, nil
}

func (d Discord) fetchGuildMember(
	ctx context.Context,
	accessToken string,
	guildID string,
) (*discordGuildMember, error) {
	endpoint := fmt.Sprintf(
		"https://discord.com/api/v10/users/@me/guilds/%s/member",
		url.PathEscape(guildID),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	var member discordGuildMember
	if err := d.doJSON(req, &member); err != nil {
		return nil, err
	}

	return &member, nil
}

func (d Discord) ValidateInstance(
	ctx context.Context,
	instanceID string,
	userID string,
) error {
	if d.BotToken == "" {
		return nil
	}

	endpoint := fmt.Sprintf(
		"https://discord.com/api/v10/applications/%s/activity-instances/%s",
		url.PathEscape(d.ClientID),
		url.PathEscape(instanceID),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+d.BotToken)

	var instance activityInstance
	if err := d.doJSON(req, &instance); err != nil {
		return fmt.Errorf("validate Activity instance: %w", err)
	}

	if instance.ApplicationID != d.ClientID || instance.InstanceID != instanceID {
		return errors.New("Discord returned a mismatched Activity instance")
	}

	for _, id := range instance.Users {
		if id == userID {
			return nil
		}
	}

	return errors.New("user is not connected to this Activity instance")
}

func (d Discord) doJSON(req *http.Request, dst any) error {
	client := d.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf(
			"Discord returned %s: %s",
			resp.Status,
			strings.TrimSpace(string(body)),
		)
	}

	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst)
}
