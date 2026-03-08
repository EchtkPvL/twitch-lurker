package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	twitch "github.com/gempir/go-twitch-irc/v4"
)

type Lurker struct {
	cfg            Config
	tg             *Telegram
	clients        []*twitch.Client
	mu             sync.Mutex
	stopCh         chan struct{}
	ignoreUsers    map[string]bool
	ignoreChannels map[string]bool
}

func NewLurker(cfg Config, tg *Telegram) *Lurker {
	ignoreUsers := make(map[string]bool)
	for _, u := range cfg.Twitch.IgnoreUsers {
		ignoreUsers[strings.ToLower(strings.TrimSpace(u))] = true
	}
	ignoreChannels := make(map[string]bool)
	for _, c := range cfg.Twitch.IgnoreChannels {
		ignoreChannels[strings.ToLower(strings.TrimSpace(c))] = true
	}
	return &Lurker{
		cfg:            cfg,
		tg:             tg,
		stopCh:         make(chan struct{}),
		ignoreUsers:    ignoreUsers,
		ignoreChannels: ignoreChannels,
	}
}

func (l *Lurker) Start() {
	channels, err := getFollowedChannels(l.cfg.Twitch.ClientID, l.cfg.Twitch.AccessToken, l.cfg.Twitch.UserID)
	if err != nil {
		log.Fatalf("failed to fetch followed channels: %v", err)
	}
	log.Printf("fetched %d followed channels", len(channels))
	l.setupClients(channels)
	go l.refreshLoop()
}

func (l *Lurker) Stop() {
	close(l.stopCh)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.clients {
		c.Disconnect()
	}
	log.Printf("all clients disconnected")
}

func (l *Lurker) refreshLoop() {
	ticker := time.NewTicker(l.cfg.Twitch.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.refresh()
		case <-l.stopCh:
			return
		}
	}
}

func (l *Lurker) refresh() {
	log.Printf("refreshing followed channels...")
	channels, err := getFollowedChannels(l.cfg.Twitch.ClientID, l.cfg.Twitch.AccessToken, l.cfg.Twitch.UserID)
	if err != nil {
		log.Printf("failed to refresh channels: %v", err)
		return
	}
	log.Printf("fetched %d followed channels, reconnecting", len(channels))

	l.mu.Lock()
	for _, c := range l.clients {
		c.Disconnect()
	}
	l.clients = nil
	l.mu.Unlock()

	l.setupClients(channels)
}

func (l *Lurker) setupClients(channels []string) {
	batches := splitBatches(channels, l.cfg.Twitch.BatchSize)
	log.Printf("setting up %d client(s) for %d channels", len(batches), len(channels))

	l.mu.Lock()
	defer l.mu.Unlock()

	username := strings.ToLower(l.cfg.Twitch.Username)

	for i, batch := range batches {
		client := twitch.NewAnonymousClient()

		client.OnPrivateMessage(func(msg twitch.PrivateMessage) {
			if l.ignoreUsers[strings.ToLower(msg.User.Name)] {
				return
			}
			channel := strings.ToLower(strings.TrimPrefix(msg.Channel, "#"))
			if l.ignoreChannels[channel] {
				return
			}
			if strings.Contains(strings.ToLower(msg.Message), username) {
				name := msg.User.DisplayName
				if name == "" {
					name = msg.User.Name
				}
				log.Printf("[#%s] <%s>: %s", msg.Channel, name, msg.Message)
				l.tg.SendMention(msg.Channel, name, msg.Message)
			}
		})

		client.OnUserNoticeMessage(func(msg twitch.UserNoticeMessage) {
			if msg.MsgID != "subgift" && msg.MsgID != "anonsubgift" {
				return
			}
			recipient := strings.ToLower(msg.MsgParams["msg-param-recipient-user-name"])
			if recipient != username {
				return
			}
			from := msg.User.DisplayName
			if from == "" {
				from = msg.User.Name
			}
			reply := fmt.Sprintf("Tausend Dank @%s !! bleedPurple CurseLit :>", from)
			log.Printf("[#%s] Sub gift from %s!", msg.Channel, from)
			l.tg.SendSubGift(msg.Channel, from, reply)
		})

		client.OnWhisperMessage(func(msg twitch.WhisperMessage) {
			name := msg.User.DisplayName
			if name == "" {
				name = msg.User.Name
			}
			log.Printf("[WHISPER] <%s>: %s", name, msg.Message)
			l.tg.SendWhisper(name, msg.Message)
		})

		client.Join(batch...)

		go func(idx int) {
			log.Printf("connecting client %d/%d (%d channels)", idx+1, len(batches), len(batch))
			if err := client.Connect(); err != nil {
				log.Printf("client %d error: %v", idx+1, err)
			}
		}(i)

		l.clients = append(l.clients, client)
	}
}

func splitBatches(items []string, size int) [][]string {
	var batches [][]string
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		batches = append(batches, items[i:end])
	}
	return batches
}
