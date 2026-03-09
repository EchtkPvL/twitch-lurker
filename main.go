package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Twitch   TwitchConfig   `yaml:"twitch"`
	Telegram TelegramConfig `yaml:"telegram"`
	Verbose  bool           `yaml:"verbose"`
}

type TwitchConfig struct {
	AccessToken     string        `yaml:"access_token"`
	Username        string        `yaml:"username"`
	MatchMode       string        `yaml:"match_mode"`
	SubGiftReply    string        `yaml:"sub_gift_reply"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	BatchSize       int           `yaml:"batch_size"`
	Keywords        []Keyword     `yaml:"keywords"`
	IgnoreUsers     []string      `yaml:"ignore_users"`
	IgnoreChannels  []string      `yaml:"ignore_channels"`
	// resolved from token validation
	ClientID string `yaml:"-"`
	UserID   string `yaml:"-"`
}

// Keyword supports both simple strings and objects in YAML:
//
//	keywords:
//	  - BieberLAN
//	  - word: NorthCon
//	    mode: exact
type Keyword struct {
	Word string `yaml:"word"`
	Mode string `yaml:"mode"`
}

func (k *Keyword) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var simple string
	if err := unmarshal(&simple); err == nil {
		k.Word = simple
		k.Mode = ""
		return nil
	}
	type plain Keyword
	return unmarshal((*plain)(k))
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   int64  `yaml:"chat_id"`
}

func main() {
	cfgPath := "config.yaml"
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		cfgPath = v
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("failed to parse config: %v", err)
	}

	if cfg.Twitch.BatchSize == 0 {
		cfg.Twitch.BatchSize = 95
	}
	if cfg.Twitch.RefreshInterval == 0 {
		cfg.Twitch.RefreshInterval = 18 * time.Hour
	}
	if cfg.Twitch.SubGiftReply == "" {
		cfg.Twitch.SubGiftReply = "@{user} !!! bleedPurple CurseLit :>"
	}

	cfg.Twitch.AccessToken = strings.TrimPrefix(cfg.Twitch.AccessToken, "oauth:")

	info, err := validateToken(cfg.Twitch.AccessToken)
	if err != nil {
		log.Fatalf("failed to validate twitch token: %v", err)
	}
	cfg.Twitch.ClientID = info.ClientID
	cfg.Twitch.UserID = info.UserID
	if cfg.Twitch.Username == "" {
		cfg.Twitch.Username = info.Login
	}
	log.Printf("authenticated as %s (id: %s)", cfg.Twitch.Username, cfg.Twitch.UserID)

	tg := NewTelegram(cfg.Telegram.BotToken, cfg.Telegram.ChatID)
	lurker := NewLurker(cfg, cfgPath, tg)
	lurker.Start()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received %v, shutting down", sig)

	lurker.Stop()
}
