package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type tokenInfo struct {
	ClientID string `json:"client_id"`
	Login    string `json:"login"`
	UserID   string `json:"user_id"`
}

func validateToken(accessToken string) (*tokenInfo, error) {
	req, err := http.NewRequest("GET", "https://id.twitch.tv/oauth2/validate", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token validation failed (%d): %s", resp.StatusCode, body)
	}

	var info tokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode validate response: %w", err)
	}
	return &info, nil
}

type followedResponse struct {
	Data       []followedChannel `json:"data"`
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
}

type followedChannel struct {
	BroadcasterLogin string `json:"broadcaster_login"`
}

func getFollowedChannels(clientID, accessToken, userID string) ([]string, error) {
	var channels []string
	cursor := ""

	for {
		reqURL := fmt.Sprintf("https://api.twitch.tv/helix/channels/followed?first=100&user_id=%s", userID)
		if cursor != "" {
			reqURL += "&after=" + cursor
		}

		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Client-ID", clientID)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("twitch API returned %d: %s", resp.StatusCode, body)
		}

		var result followedResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		resp.Body.Close()

		for _, ch := range result.Data {
			channels = append(channels, ch.BroadcasterLogin)
		}

		if result.Pagination.Cursor == "" {
			break
		}
		cursor = result.Pagination.Cursor
	}

	return channels, nil
}
