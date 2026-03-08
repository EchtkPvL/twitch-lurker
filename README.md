# Twitch Lurker

A lightweight Go service that lurks in all your followed Twitch channels and sends Telegram notifications when someone mentions your name or gifts you a sub.

## Features

- **Mention alerts** — notifies you via Telegram when your username is mentioned in any followed channel
- **Sub gift alerts** — detects gifted subs directed at you and sends a notification
- **Whisper forwarding** — forwards incoming Twitch whispers to Telegram
- **Auto-discovery** — fetches your followed channels from the Twitch Helix API
- **Periodic refresh** — re-fetches followed channels on a configurable interval (default: 18h)
- **Ignore lists** — skip specific users or channels from triggering notifications
- **Batched connections** — splits channels across multiple IRC clients to stay within Twitch limits

## Setup

1. Copy the example files and fill in your credentials:
   ```bash
   cp config.yaml.example config.yaml
   ```

2. Edit `config.yaml` with your Twitch and Telegram credentials.

3. Run with Docker Compose:
   ```bash
   make build
   ```

## Deployment

```bash
make deploy VERSION=v1.0.0
```

This will show `git status`, prompt for confirmation, test-build the Docker image, push to the remote, and tag a release that triggers GitHub Actions to build and push the image to GHCR.

Other targets:

| Command | Description |
|---|---|
| `make build` | Build and run locally with Docker Compose |
| `make logs` | Follow container logs |
| `make test-build` | Test the Docker image build locally |
| `make release VERSION=v1.0.0` | Tag and push (triggers GHCR build) |

## Configuration

See `config.yaml.example` for all options:

```yaml
twitch:
  user_id: "your-twitch-user-id"
  username: your_twitch_username
  client_id: "your-client-id"
  access_token: "your-access-token"
  refresh_interval: 18h
  batch_size: 95
  ignore_users:
    - streamelements
    - nightbot
  ignore_channels: []

telegram:
  bot_token: "your-telegram-bot-token"
  chat_id: 123456789
```

| Field | Description | Default |
|---|---|---|
| `twitch.user_id` | Your Twitch user ID (numeric) | — |
| `twitch.username` | Your Twitch username (used for mention matching) | — |
| `twitch.client_id` | Twitch application client ID | — |
| `twitch.access_token` | Twitch OAuth access token | — |
| `twitch.refresh_interval` | How often to re-fetch followed channels | `18h` |
| `twitch.batch_size` | Max channels per IRC client | `95` |
| `twitch.ignore_users` | Usernames to ignore (bots, etc.) | `[]` |
| `twitch.ignore_channels` | Channels to ignore | `[]` |
| `telegram.bot_token` | Telegram Bot API token | — |
| `telegram.chat_id` | Telegram chat ID to send notifications to | — |

## License

[AGPL-3.0](LICENSE)
