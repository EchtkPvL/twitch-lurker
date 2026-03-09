package main

import (
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
	keywords       []resolvedKeyword
	ignoreUsers    map[string]bool
	ignoreChannels map[string]bool
}

type resolvedKeyword struct {
	word string
	mode string // "contains" or "exact"
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
	return &Lurker{
		cfg:            cfg,
		cfgPath:        cfgPath,
		tg:             tg,
		stopCh:         make(chan struct{}),
		keywords:       resolveKeywords(cfg.Twitch.Keywords),
		ignoreUsers:    ignoreUsers,
		ignoreChannels: ignoreChannels,
	}
}

func resolveKeywords(keywords []Keyword) []resolvedKeyword {
	resolved := make([]resolvedKeyword, len(keywords))
	for i, k := range keywords {
		mode := k.Mode
		if mode == "" {
			mode = "contains"
		}
		resolved[i] = resolvedKeyword{
			word: strings.ToLower(strings.TrimSpace(k.Word)),
			mode: mode,
		}
	}
	return resolved
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
	usernameMode := l.cfg.Twitch.MatchMode
	if usernameMode == "" {
		usernameMode = "contains"
	}

	for i, batch := range batches {
		client := twitch.NewAnonymousClient()

		client.OnPrivateMessage(func(msg twitch.PrivateMessage) {
			msgLower := strings.ToLower(msg.Message)
			if !l.matchesKeywords(msgLower, username, usernameMode) {
				return
			}
			log.Printf("[#%s] <%s>: %s", msg.Channel, msg.User.Name, msg.Message)
			if l.cfg.Verbose {
				log.Printf("[VERBOSE] %s", msg.Raw)
			}
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
			log.Printf("[#%s] Sub gift from %s!", msg.Channel, msg.User.Name)
			if l.cfg.Verbose {
				log.Printf("[VERBOSE] %s", msg.Raw)
			}
			l.mu.RLock()
			replyTpl := l.cfg.Twitch.SubGiftReply
			l.mu.RUnlock()
			l.tg.SendSubGift(msg.Channel, msg.User.Name, msg.User.DisplayName, replyTpl)
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

	keywords := resolveKeywords(newCfg.Twitch.Keywords)

	subGiftReply := newCfg.Twitch.SubGiftReply
	if subGiftReply == "" {
		subGiftReply = "@{user} !!! bleedPurple CurseLit :>"
	}

	l.mu.Lock()
	l.cfg.Twitch.SubGiftReply = subGiftReply
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

func (l *Lurker) matchesKeywords(msgLower, username, usernameMode string) bool {
	if matchWord(msgLower, username, usernameMode) {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, kw := range l.keywords {
		if matchWord(msgLower, kw.word, kw.mode) {
			return true
		}
	}
	return false
}

func matchWord(msg, word, mode string) bool {
	if word == "" {
		return false
	}
	if mode == "exact" {
		return containsExact(msg, word)
	}
	return strings.Contains(msg, word)
}

func containsExact(msg, word string) bool {
	idx := 0
	for {
		pos := strings.Index(msg[idx:], word)
		if pos == -1 {
			return false
		}
		start := idx + pos
		end := start + len(word)
		startOK := start == 0 || !isAlphanumeric(msg[start-1])
		endOK := end == len(msg) || !isAlphanumeric(msg[end])
		if startOK && endOK {
			return true
		}
		idx = start + 1
	}
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
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
