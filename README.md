# Trustroots Nostr Email Contact Tool

Connects to MongoDB to find Trustroots users with nostrNpub set and listen for mentions on Nostr relays.

Will be redundant when
1. a working app has push notifications
2. trustroots is completely decentralized.

For now it's probably a good way to move nostroots forward.

## Setup

### Docker (Recommended)
```bash
cp env.example .env
cp config.json.example config.json
# Edit config.json with your settings
docker-compose up -d
```

### Local Development
1. Copy config: `cp config.json.example config.json`
2. Install deps: `go mod tidy`
3. Edit `config.json` with the sending npub/nsec keys

## Usage

```bash
go run main.go                    # Show summary
go run main.go --list-users      # List users in categories  
go run main.go --nostr-listen    # Listen for mentions
go run main.go --test --send-to-npub <npub> --msg "<message>"  # Send test note
```

## Config

```json
{
  "mongodb": {
    "uri": "mongodb://localhost:27017",
    "database": "trustroots"
  },
  "sender-npub": "profile-npub",
  "sender-nsec": "profile-nsec",
  "sender-email": "noreply@trustroots.org",
  "relays": ["wss://relay1.com", "wss://relay2.com"],
  "smtp": {
    "host": "smtp.gmail.com",
    "port": 587,
    "username": "your-email@gmail.com",
    "password": "your-app-password",
    "from_name": "Trustroots Nostr"
  }
}
```

## Docker Commands

```bash
docker-compose up -d          # Start
docker-compose logs -f        # View logs
docker-compose down           # Stop
docker-compose restart        # Restart
```

## License

Unlicense - Public Domain