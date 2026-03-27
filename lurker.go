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
	followClients  []*twitch.Client
	topClients     []*twitch.Client
	topJoined      map[string]bool // currently joined top stream channels (lowercased)
	followedSet    map[string]bool // followed channels (lowercased), used for dedup
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
		topJoined:      make(map[string]bool),
		followedSet:    make(map[string]bool),
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
	followed, err := getFollowedChannels(l.cfg.Twitch.ClientID, l.cfg.Twitch.AccessToken, l.cfg.Twitch.UserID)
	if err != nil {
		log.Fatalf("failed to fetch followed channels: %v", err)
	}
	log.Printf("fetched %d followed channels", len(followed))

	l.followedSet = toSet(followed)
	l.followClients = l.createClients(followed, "followed")

	if l.cfg.Twitch.TopStreams != nil {
		topChannels := l.fetchTopStreams()
		deduped := l.dedup(topChannels)
		l.topClients = l.createClients(deduped, "top")
		l.topJoined = toSet(deduped)
		go l.topStreamsLoop()
	}

	go l.refreshLoop()
	go l.watchConfig()
}

func (l *Lurker) Stop() {
	close(l.stopCh)
	for _, c := range l.followClients {
		c.Disconnect()
	}
	for _, c := range l.topClients {
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
			l.refreshFollowed()
		case <-l.stopCh:
			return
		}
	}
}

func (l *Lurker) refreshFollowed() {
	log.Printf("refreshing followed channels...")
	followed, err := getFollowedChannels(l.cfg.Twitch.ClientID, l.cfg.Twitch.AccessToken, l.cfg.Twitch.UserID)
	if err != nil {
		log.Printf("failed to refresh channels: %v", err)
		return
	}
	log.Printf("fetched %d followed channels, reconnecting followed pool", len(followed))

	l.followedSet = toSet(followed)

	// disconnect old followed clients
	for _, c := range l.followClients {
		c.Disconnect()
	}
	l.followClients = l.createClients(followed, "followed")

	// also update top stream clients to dedup against new followed list
	if l.cfg.Twitch.TopStreams != nil {
		l.refreshTopStreams()
	}
}

func (l *Lurker) topStreamsLoop() {
	ts := l.cfg.Twitch.TopStreams
	interval := ts.RefreshInterval
	if interval == 0 {
		interval = 30 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.refreshTopStreams()
		case <-l.stopCh:
			return
		}
	}
}

func (l *Lurker) refreshTopStreams() {
	channels := l.fetchTopStreams()
	deduped := l.dedup(channels)
	newSet := toSet(deduped)

	// compute diff
	var toJoin, toDepart []string
	for ch := range newSet {
		if !l.topJoined[ch] {
			toJoin = append(toJoin, ch)
		}
	}
	for ch := range l.topJoined {
		if !newSet[ch] {
			toDepart = append(toDepart, ch)
		}
	}

	if len(toJoin) == 0 && len(toDepart) == 0 {
		return
	}

	log.Printf("top streams: joining %d, departing %d channels", len(toJoin), len(toDepart))

	// depart removed channels
	for _, ch := range toDepart {
		for _, c := range l.topClients {
			c.Depart(ch)
		}
	}

	// join new channels, spread across existing clients
	if len(toJoin) > 0 {
		if len(l.topClients) == 0 {
			// no top clients yet, create them
			l.topClients = l.createClients(deduped, "top")
		} else {
			for i, ch := range toJoin {
				idx := i % len(l.topClients)
				l.topClients[idx].Join(ch)
			}
		}
	}

	l.topJoined = newSet
}

func (l *Lurker) fetchTopStreams() []string {
	ts := l.cfg.Twitch.TopStreams
	if ts == nil {
		return nil
	}
	batches := ts.Batches
	if batches <= 0 {
		batches = 2
	}
	limit := batches * l.cfg.Twitch.BatchSize
	channels, err := getTopStreams(l.cfg.Twitch.ClientID, l.cfg.Twitch.AccessToken, ts.Languages, ts.GameIDs, limit)
	if err != nil {
		log.Printf("failed to fetch top streams: %v", err)
		return nil
	}
	log.Printf("fetched %d top stream channels", len(channels))
	return channels
}

// dedup removes channels that are already in the followed set
func (l *Lurker) dedup(channels []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, ch := range channels {
		key := strings.ToLower(ch)
		if !l.followedSet[key] && !seen[key] {
			seen[key] = true
			result = append(result, ch)
		}
	}
	return result
}

// createClients creates IRC clients for the given channels and starts them
func (l *Lurker) createClients(channels []string, pool string) []*twitch.Client {
	if len(channels) == 0 {
		return nil
	}
	batches := splitBatches(channels, l.cfg.Twitch.BatchSize)
	log.Printf("setting up %d %s client(s) for %d channels", len(batches), pool, len(channels))

	username := strings.ToLower(l.cfg.Twitch.Username)
	usernameMode := l.cfg.Twitch.MatchMode
	if usernameMode == "" {
		usernameMode = "contains"
	}

	var clients []*twitch.Client

	for i, batch := range batches {
		client := twitch.NewAnonymousClient()
		clientTag := fmt.Sprintf("%s:%d", pool, i+1)

		client.OnPrivateMessage(func(msg twitch.PrivateMessage) {
			msgLower := strings.ToLower(msg.Message)
			if !l.matchesKeywords(msgLower, username, usernameMode) {
				return
			}
			log.Printf("[#%s] <%s>: %s", msg.Channel, msg.User.Name, msg.Message)
			if l.cfg.Verbose {
				log.Printf("[VERBOSE] [%s] %s", clientTag, msg.Raw)
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
				log.Printf("[VERBOSE] [%s] %s", clientTag, msg.Raw)
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

		go func(tag string, n int) {
			log.Printf("connecting %s (%d channels)", tag, n)
			if err := client.Connect(); err != nil {
				log.Printf("%s error: %v", tag, err)
			}
		}(clientTag, len(batch))

		clients = append(clients, client)
	}

	return clients
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

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[strings.ToLower(item)] = true
	}
	return s
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
