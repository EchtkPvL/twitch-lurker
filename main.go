package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Twitch   TwitchConfig   `yaml:"twitch"`
	Telegram TelegramConfig `yaml:"telegram"`
}

type TwitchConfig struct {
	UserID          string        `yaml:"user_id"`
	Username        string        `yaml:"username"`
	ClientID        string        `yaml:"client_id"`
	AccessToken     string        `yaml:"access_token"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	BatchSize       int           `yaml:"batch_size"`
	IgnoreUsers     []string      `yaml:"ignore_users"`
	IgnoreChannels  []string      `yaml:"ignore_channels"`
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
	tg := NewTelegram(cfg.Telegram.BotToken, cfg.Telegram.ChatID)
	lurker := NewLurker(cfg, tg)
	lurker.Start()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received %v, shutting down", sig)

	lurker.Stop()
}
