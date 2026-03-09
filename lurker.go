package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	twitch "github.com/gempir/go-twitch-irc/v4"
	"gopkg.in/yaml.v3"
)

type Lurker struct {
	cfg            Config
	cfgPath        string
	tg             *Telegram
	clients        []*twitch.Client
	mu             sync.RWMutex
	stopCh         chan struct{}
	keywords       []string
	ignoreUsers    map[string]bool
	ignoreChannels map[string]bool
}

func NewLurker(cfg Config, cfgPath string, tg *Telegram) *Lurker {
	ignoreUsers := make(map[string]bool)
	for _, u := range cfg.Twitch.IgnoreUsers {
		ignoreUsers[strings.ToLower(strings.TrimSpace(u))] = true
	}
	ignoreChannels := make(map[string]bool)
	for _, c := range cfg.Twitch.IgnoreChannels {
		ignoreChannels[strings.ToLower(strings.TrimSpace(c))] = true
	}
	keywords := make([]string, len(cfg.Twitch.Keywords))
	for i, k := range cfg.Twitch.Keywords {
		keywords[i] = strings.ToLower(strings.TrimSpace(k))
	}
	return &Lurker{
		cfg:            cfg,
		cfgPath:        cfgPath,
		tg:             tg,
		stopCh:         make(chan struct{}),
		keywords:       keywords,
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
	go l.watchConfig()
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
			msgLower := strings.ToLower(msg.Message)
			if !l.matchesKeywords(msgLower, username) {
				return
			}
			log.Printf("[#%s] <%s>: %s", msg.Channel, msg.User.Name, msg.Message)
			l.mu.RLock()
			ignoreUser := l.ignoreUsers[strings.ToLower(msg.User.Name)]
			ignoreChan := l.ignoreChannels[strings.ToLower(strings.TrimPrefix(msg.Channel, "#"))]
			l.mu.RUnlock()
			if ignoreUser || ignoreChan {
				return
			}
			l.tg.SendMention(msg.Channel, msg.User.Name, msg.User.DisplayName, msg.Message)
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

func (l *Lurker) reloadConfig() {
	data, err := os.ReadFile(l.cfgPath)
	if err != nil {
		log.Printf("config reload: failed to read file: %v", err)
		return
	}

	var newCfg Config
	if err := yaml.Unmarshal(data, &newCfg); err != nil {
		log.Printf("config reload: invalid config, keeping old: %v", err)
		return
	}

	if newCfg.Twitch.AccessToken == "" {
		log.Printf("config reload: invalid config (empty access_token), keeping old")
		return
	}
	if newCfg.Telegram.BotToken == "" || newCfg.Telegram.ChatID == 0 {
		log.Printf("config reload: invalid config (missing telegram settings), keeping old")
		return
	}

	ignoreUsers := make(map[string]bool)
	for _, u := range newCfg.Twitch.IgnoreUsers {
		ignoreUsers[strings.ToLower(strings.TrimSpace(u))] = true
	}
	ignoreChannels := make(map[string]bool)
	for _, c := range newCfg.Twitch.IgnoreChannels {
		ignoreChannels[strings.ToLower(strings.TrimSpace(c))] = true
	}

	keywords := make([]string, len(newCfg.Twitch.Keywords))
	for i, k := range newCfg.Twitch.Keywords {
		keywords[i] = strings.ToLower(strings.TrimSpace(k))
	}

	l.mu.Lock()
	l.keywords = keywords
	l.ignoreUsers = ignoreUsers
	l.ignoreChannels = ignoreChannels
	l.mu.Unlock()

	l.tg.Update(newCfg.Telegram.BotToken, newCfg.Telegram.ChatID)

	log.Printf("config reloaded: %d keywords, %d ignored users, %d ignored channels",
		len(keywords), len(ignoreUsers), len(ignoreChannels))
}

func (l *Lurker) watchConfig() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config watch: failed to create watcher: %v", err)
		return
	}

	go func() {
		defer watcher.Close()
		var debounce <-chan time.Time
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					debounce = time.After(500 * time.Millisecond)
				}
			case <-debounce:
				l.reloadConfig()
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("config watch error: %v", err)
			case <-l.stopCh:
				return
			}
		}
	}()

	if err := watcher.Add(l.cfgPath); err != nil {
		log.Printf("config watch: failed to watch %s: %v", l.cfgPath, err)
	} else {
		log.Printf("watching %s for changes", l.cfgPath)
	}
}

func (l *Lurker) matchesKeywords(msgLower, username string) bool {
	if strings.Contains(msgLower, username) {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, kw := range l.keywords {
		if strings.Contains(msgLower, kw) {
			return true
		}
	}
	return false
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
