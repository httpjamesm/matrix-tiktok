<div align="center">

# matrix-tiktok

![Matrix](https://img.shields.io/badge/matrix-%23000000?style=for-the-badge&logo=matrix&logoColor=white)
![Go](https://img.shields.io/badge/go-%2300ADD8.svg?style=for-the-badge&logo=go&logoColor=white)
![Docker](https://img.shields.io/badge/docker-%230db7ed.svg?style=for-the-badge&logo=docker&logoColor=white)

</div>

A Matrix bridge for TikTok Messages. Intended for usage with Beeper.

## Features

- [ ] Messages
  - [x] Text
  - [x] Images
  - [x] Videos
  - [x] Stickers
  - [ ] Voice Notes
  - [x] TikToks
- [x] Reactions
- [x] Typing indicators
- [x] Mark conversation as read
- [x] Replies
- [x] Read receipts
- [x] Group chats
- [x] Delete message for everyone

## Running

### Docker (GHCR)

Container images are published to **GitHub Container Registry**:

`ghcr.io/httpjamesm/matrix-tiktok`

Use the image tag that matches the [GitHub release](https://github.com/httpjamesm/matrix-tiktok/releases) you want (for example `v1.0.0`). Mount your `bbctl`-generated config at `/data/config.yaml`:

```bash
docker pull ghcr.io/httpjamesm/matrix-tiktok:<tag>
docker run --rm \
  -v /path/to/config.yaml:/data/config.yaml:ro \
  ghcr.io/httpjamesm/matrix-tiktok:<tag>
```

With Beeper’s default `homeserver.websocket` setup you usually do **not** need to publish an appservice port; add `-p host:container` only if your config uses inbound appservice HTTP instead of the websocket.

### From source

#### 1. Install `bbctl`

Install `bbctl` from Beeper's [documentation](https://developers.beeper.com/bridges/self-hosting/#step-1-download-bbctl).

Run `bbctl login` to log into your Beeper account.

#### 2. Generate configuration

Run `bbctl config --type bridgev2 -o config.yaml sh-tiktok` to create a configuration in the project root directory.

#### 3. Run with config

Run `go run cmd/matrix-tiktok/main.go -c config.yaml`.

#### 4. Login

Send an invitation to `@sh-tiktokbot:beeper.local` on Beeper and send "login".

Sign into TikTok Messages, grab your cookie string, and paste it in.

## Legal Disclaimer

This project involves reverse engineering TikTok's private API for the sole purpose of
enabling interoperability with the [Matrix](https://matrix.org/) protocol. No TikTok
source code, assets, or copyrighted materials are reproduced or distributed by this project.

This project is **legally protected** by both Canadian and United States law. Attempts to take down this project are illegal and may incur substantial
indemnity costs to you.

[Read more](./disclaimer.md)
