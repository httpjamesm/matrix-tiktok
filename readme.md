# matrix-tiktok

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

### 1. Install `bbctl`

Install `bbctl` from Beeper's [documentation](https://developers.beeper.com/bridges/self-hosting/#step-1-download-bbctl).

Run `bbctl login` to log into your Beeper account.

### 2. Generate configuration

Run `bbctl config --type bridgev2 -o config.yaml sh-tiktok` to create a configuration in the project root directory.

### 3. Run with config

Run `go run cmd/matrix-tiktok/main.go -c config.yaml`.

### 4. Login

Send an invitation to `@sh-tiktokbot:beeper.local` on Beeper and send "login".

Sign into TikTok Messages, grab your cookie string, and paste it in.

## Legal Disclaimer

This project involves reverse engineering TikTok's private API for the sole purpose of
enabling interoperability with the [Matrix](https://matrix.org/) protocol. No TikTok
source code, assets, or copyrighted materials are reproduced or distributed by this project.

This project is **legally protected** by both Canadian and United States law. Attempts to take down this project are illegal and may incur substantial
indemnity costs to you.

[Read more](./disclaimer.md)
