# Trustroots Nostr Email Contact Tool

Connects to MongoDB to find Trustroots users with nostrNpub set and listen for mentions on Nostr relays.


Will be redundant when
1. a working app has push notificaitions
2. trustroots is completely decentralized.

For now it's probably a good way to move nostroots forward.


## Setup

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
  "relays": ["wss://relay1.com", "wss://relay2.com"]
}
```

## License

Unlicense - Public Domain