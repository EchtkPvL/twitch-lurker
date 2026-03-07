package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

type Telegram struct {
	token  string
	chatID int64
}

func NewTelegram(token string, chatID int64) *Telegram {
	return &Telegram{token: token, chatID: chatID}
}

func (t *Telegram) SendMention(channel, user, message string) {
	text := fmt.Sprintf("%s &lt;<b>%s</b>&gt;:\n%s",
		channelLink(channel), escHTML(user), escHTML(message))
	t.send(text)
}

func (t *Telegram) SendSubGift(channel, from, reply string) {
	text := fmt.Sprintf("Sub gift in %s from <b>%s</b>!\n<code>%s</code>",
		channelLink(channel), escHTML(from), escHTML(reply))
	t.send(text)
}

func (t *Telegram) SendWhisper(from, message string) {
	text := fmt.Sprintf("Whisper from <b>%s</b>:\n%s",
		escHTML(from), escHTML(message))
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

func (t *Telegram) send(text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)
	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id":    {fmt.Sprintf("%d", t.chatID)},
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
