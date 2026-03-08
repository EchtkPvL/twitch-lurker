package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type Telegram struct {
	mu     sync.RWMutex
	token  string
	chatID int64
}

func NewTelegram(token string, chatID int64) *Telegram {
	return &Telegram{token: token, chatID: chatID}
}

func (t *Telegram) Update(token string, chatID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = token
	t.chatID = chatID
}

func (t *Telegram) SendMention(channel, login, displayName, message string) {
	copyName := displayName
	if strings.ToLower(displayName) != strings.ToLower(login) {
		copyName = login
	}
	text := fmt.Sprintf("%s &lt;<code>%s</code>&gt;:\n%s",
		channelLink(channel), escHTML(copyName), escAtMention(escHTML(message)))
	t.send(text)
}

func (t *Telegram) SendSubGift(channel, from, reply string) {
	text := fmt.Sprintf("Sub gift in %s from <code>%s</code>!\n<code>%s</code>",
		channelLink(channel), escHTML(from), escHTML(reply))
	t.send(text)
}

func (t *Telegram) SendWhisper(from, message string) {
	text := fmt.Sprintf("Whisper from <b>%s</b>:\n%s",
		escHTML(from), escAtMention(escHTML(message)))
	t.send(text)
}

func channelLink(channel string) string {
	ch := strings.TrimPrefix(channel, "#")
	return fmt.Sprintf("<a href=\"https://www.twitch.tv/%s\">#%s</a>", ch, escHTML(ch))
}

func escHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func escAtMention(s string) string {
	return strings.ReplaceAll(s, "@", "@\u200b")
}

func (t *Telegram) send(text string) {
	t.mu.RLock()
	token, chatID := t.token, t.chatID
	t.mu.RUnlock()

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"text":       {text},
		"parse_mode": {"HTML"},
		"link_preview_options": {`{"is_disabled":true}`},
	})
	if err != nil {
		log.Printf("telegram error: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("telegram API returned %d", resp.StatusCode)
	}
}
