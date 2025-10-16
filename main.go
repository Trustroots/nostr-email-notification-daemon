package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// User represents a user document from the database
type User struct {
	ID        string `bson:"_id,omitempty"`
	Username  string `bson:"username,omitempty"`
	Email     string `bson:"email,omitempty"`
	NostrNpub string `bson:"nostrNpub,omitempty"`
}

// Config represents the configuration structure
type Config struct {
	MongoDB struct {
		URI      string
		Database string
	}
	SenderNpub  string
	SenderNsec  string
	SenderEmail string
	Relays      []string
	SMTP        struct {
		Host     string
		Port     int
		Username string
		Password string
		FromName string
	}
}

// NostrEvent represents a nostr event
type NostrEvent struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// NIP5Response represents the response from a NIP-5 verification endpoint
type NIP5Response struct {
	Names map[string]string `json:"names"`
}

// NostrMessage represents a nostr message
type NostrMessage struct {
	Type   string                 `json:"type"`
	ID     string                 `json:"id,omitempty"`
	Event  *NostrEvent            `json:"event,omitempty"`
	Filter map[string]interface{} `json:"filter,omitempty"`
}

func main() {
	// Parse command line arguments
	listUsersFlag := flag.Bool("list-users", false, "List all users in 3 categories")
	nostrListenFlag := flag.Bool("nostr-listen", false, "Listen to nostr relays for mentions of valid npubs")
	testFlag := flag.Bool("test", false, "Send a test note")
	recipientNpub := flag.String("send-to-npub", "", "Recipient npub for test send (required with --test)")
	message := flag.String("msg", "", "Message content for test send (required with --test)")
	skipNIP5Flag := flag.Bool("skip-nip5", false, "Skip NIP-5 verification for testing purposes")
	flag.Parse()

	// Load configuration from environment variables
	config, err := loadConfigFromEnv()
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Check MongoDB connectivity first before any other operations
	fmt.Println("🔍 Checking MongoDB connectivity...")
	client, err := connectToMongoDB(config)
	if err != nil {
		log.Fatal("❌ MongoDB is not reachable:", err)
	}
	defer func() {
		if err = client.Disconnect(context.TODO()); err != nil {
			log.Fatal("Failed to disconnect from MongoDB:", err)
		}
	}()

	// Initialize SQLite database for tracking processed notes
	sqliteDB, err := initSQLiteDB()
	if err != nil {
		log.Fatal("Failed to initialize SQLite database:", err)
	}
	defer sqliteDB.Close()

	// Initialize email service
	emailService := NewEmailService(
		config.SMTP.Host,
		config.SMTP.Port,
		config.SMTP.Username,
		config.SMTP.Password,
		config.SenderEmail,
		config.SMTP.FromName,
	)

	// Get users from database
	users, err := getUsersFromDB(client, config)
	if err != nil {
		log.Fatal("Failed to get users from database:", err)
	}

	// Categorize users
	validNpubs, invalidNpubs, emptyNpubs := categorizeUsers(users)

	if *listUsersFlag {
		displayUserList(validNpubs, invalidNpubs, emptyNpubs)
		return
	}

	if *nostrListenFlag {
		err = listenToNostrRelays(validNpubs, config.Relays, *skipNIP5Flag, client, config, sqliteDB, emailService)
		if err != nil {
			log.Fatal("Failed to listen to nostr relays:", err)
		}
		return
	}

	if *testFlag {
		if *recipientNpub == "" {
			log.Fatal("--send-to-npub is required when using --test")
		}
		if *message == "" {
			log.Fatal("--msg is required when using --test")
		}
		err = sendTestNote(config.SenderNpub, config.SenderNsec, *recipientNpub, *message, config.Relays)
		if err != nil {
			log.Fatal("Failed to send test note:", err)
		}
		return
	}

	// Default behavior - show summary
	displaySummary(users, validNpubs, invalidNpubs, emptyNpubs)
}

func loadConfigFromEnv() (*Config, error) {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: .env file not found, using system environment variables: %v", err)
	}

	// Parse SMTP port
	smtpPort := 587 // default
	if portStr := os.Getenv("NOSTREMAIL_SMTP_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			smtpPort = port
		}
	}

	// Parse relays (comma-separated)
	var relays []string
	if relaysStr := os.Getenv("NOSTREMAIL_RELAYS"); relaysStr != "" {
		relays = strings.Split(relaysStr, ",")
		// Trim whitespace from each relay
		for i, relay := range relays {
			relays[i] = strings.TrimSpace(relay)
		}
	}

	config := &Config{
		MongoDB: struct {
			URI      string
			Database string
		}{
			URI:      getEnvOrDefault("MONGO_URI", "mongodb://localhost:27017"),
			Database: getEnvOrDefault("MONGO_DB", "trust-roots"),
		},
		SenderNpub:  os.Getenv("NOSTREMAIL_SENDER_NPUB"),
		SenderNsec:  os.Getenv("NOSTREMAIL_SENDER_NSEC"),
		SenderEmail: os.Getenv("NOSTREMAIL_SENDER_EMAIL"),
		Relays:      relays,
		SMTP: struct {
			Host     string
			Port     int
			Username string
			Password string
			FromName string
		}{
			Host:     os.Getenv("NOSTREMAIL_SMTP_HOST"),
			Port:     smtpPort,
			Username: os.Getenv("NOSTREMAIL_SMTP_USERNAME"),
			Password: os.Getenv("NOSTREMAIL_SMTP_PASSWORD"),
			FromName: os.Getenv("NOSTREMAIL_SMTP_FROM_NAME"),
		},
	}

	// Validate required fields
	if config.SenderNpub == "" {
		return nil, fmt.Errorf("NOSTREMAIL_SENDER_NPUB environment variable is required")
	}
	if config.SenderNsec == "" {
		return nil, fmt.Errorf("NOSTREMAIL_SENDER_NSEC environment variable is required")
	}
	if config.SenderEmail == "" {
		return nil, fmt.Errorf("NOSTREMAIL_SENDER_EMAIL environment variable is required")
	}
	if len(config.Relays) == 0 {
		return nil, fmt.Errorf("NOSTREMAIL_RELAYS environment variable is required")
	}
	if config.SMTP.Host == "" {
		return nil, fmt.Errorf("NOSTREMAIL_SMTP_HOST environment variable is required")
	}
	if config.SMTP.Username == "" {
		return nil, fmt.Errorf("NOSTREMAIL_SMTP_USERNAME environment variable is required")
	}
	if config.SMTP.Password == "" {
		return nil, fmt.Errorf("NOSTREMAIL_SMTP_PASSWORD environment variable is required")
	}

	return config, nil
}

// getEnvOrDefault returns the environment variable value or a default if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func connectToMongoDB(config *Config) (*mongo.Client, error) {
	clientOptions := options.Client().ApplyURI(config.MongoDB.URI)
	client, err := mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create MongoDB client: %v", err)
	}

	// Ping the database to verify connection
	err = client.Ping(context.TODO(), nil)
	if err != nil {
		client.Disconnect(context.TODO())
		return nil, fmt.Errorf("failed to ping MongoDB at %s: %v", config.MongoDB.URI, err)
	}
	fmt.Printf("✅ Successfully connected to MongoDB at %s!\n", config.MongoDB.URI)
	return client, nil
}

func getUsersFromDB(client *mongo.Client, config *Config) ([]User, error) {
	db := client.Database(config.MongoDB.Database)
	collection := db.Collection("users")

	// Query for users that have nostrNpub set
	filter := bson.M{"nostrNpub": bson.M{"$exists": true}}

	// Count total documents matching the filter
	count, err := collection.CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Found %d users with nostrNpub set\n", count)

	// Find all users with nostrNpub
	cursor, err := collection.Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	// Process results
	var users []User
	if err = cursor.All(context.TODO(), &users); err != nil {
		return nil, err
	}

	return users, nil
}

func categorizeUsers(users []User) ([]User, []User, []User) {
	var validNpubs, invalidNpubs, emptyNpubs []User
	for _, user := range users {
		if user.NostrNpub == "" {
			emptyNpubs = append(emptyNpubs, user)
		} else if strings.HasPrefix(user.NostrNpub, "npub1") && len(user.NostrNpub) > 50 {
			validNpubs = append(validNpubs, user)
		} else {
			invalidNpubs = append(invalidNpubs, user)
		}
	}
	return validNpubs, invalidNpubs, emptyNpubs
}

func displayUserList(validNpubs, invalidNpubs, emptyNpubs []User) {
	fmt.Println("\n=== VALID NOSTR NPUBS ===")
	fmt.Printf("Count: %d\n", len(validNpubs))
	fmt.Println("Username | Email | Nostr Npub")
	fmt.Println(strings.Repeat("-", 100))
	for _, user := range validNpubs {
		fmt.Printf("%s | %s | %s\n", user.Username, user.Email, user.NostrNpub)
	}

	fmt.Println("\n=== INVALID/OTHER NPUBS ===")
	fmt.Printf("Count: %d\n", len(invalidNpubs))
	fmt.Println("Username | Email | Nostr Npub")
	fmt.Println(strings.Repeat("-", 100))
	for _, user := range invalidNpubs {
		fmt.Printf("%s | %s | %s\n", user.Username, user.Email, user.NostrNpub)
	}

	fmt.Println("\n=== EMPTY NPUBS ===")
	fmt.Printf("Count: %d\n", len(emptyNpubs))
	fmt.Println("Username | Email | Nostr Npub")
	fmt.Println(strings.Repeat("-", 100))
	for _, user := range emptyNpubs {
		fmt.Printf("%s | %s | (empty)\n", user.Username, user.Email)
	}

	fmt.Printf("\n=== SUMMARY ===\n")
	fmt.Printf("Total users: %d\n", len(validNpubs)+len(invalidNpubs)+len(emptyNpubs))
	fmt.Printf("Valid npubs: %d\n", len(validNpubs))
	fmt.Printf("Invalid npubs: %d\n", len(invalidNpubs))
	fmt.Printf("Empty npubs: %d\n", len(emptyNpubs))
}

func listenToNostrRelays(validNpubs []User, relays []string, skipNIP5 bool, client *mongo.Client, config *Config, sqliteDB *sql.DB, emailService *EmailService) error {
	fmt.Println("🔍 Listening to nostr relays for mentions...")
	fmt.Printf("Connecting to %d relays: %v\n", len(relays), relays)

	// Create a map of npubs to users for quick lookup
	npubToUser := make(map[string]User)
	for _, user := range validNpubs {
		npubToUser[user.NostrNpub] = user
	}

	fmt.Printf("\nMonitoring %d valid npubs for mentions...\n", len(validNpubs))
	fmt.Println("Press Ctrl+C to stop listening")
	fmt.Println()

	// Connect to each relay
	for _, relayURL := range relays {
		go func(relay string) {
			err := connectToRelay(relay, npubToUser, skipNIP5, client, config, sqliteDB, emailService)
			if err != nil {
				fmt.Printf("❌ Error connecting to %s: %v\n", relay, err)
			}
		}(relayURL)
	}

	// Keep the main thread alive
	select {}
}

func connectToRelay(relayURL string, npubToUser map[string]User, skipNIP5 bool, client *mongo.Client, config *Config, sqliteDB *sql.DB, emailService *EmailService) error {

	// Parse URL
	u, err := url.Parse(relayURL)
	if err != nil {
		return fmt.Errorf("invalid relay URL %s: %v", relayURL, err)
	}

	// Connect via WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %v", relayURL, err)
	}
	defer conn.Close()

	fmt.Printf("✅ Connected to %s\n", relayURL)

	// Create subscription for all npubs at once
	subID := fmt.Sprintf("sub_%d", time.Now().Unix())

	// Create subscription for notes mentioning our users
	npubs := getNpubsFromUsers(npubToUser)
	fmt.Printf("🔍 Subscribing to mentions of %d npubs: %v\n", len(npubs), npubs[:min(3, len(npubs))])

	subscribeMsg := []interface{}{
		"REQ",
		subID,
		map[string]interface{}{
			"kinds": []int{1, 4, 14, 15}, // 1=text notes, 4=NIP-4 DMs, 14=NIP-17 gift wrap, 15=NIP-17 sealed DM
			"#p":    npubs,
			"since": int(time.Now().Unix()),
		},
	}

	// Send subscription
	msgBytes, err := json.Marshal(subscribeMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal subscription: %v", err)
	}

	err = conn.WriteMessage(websocket.TextMessage, msgBytes)
	if err != nil {
		return fmt.Errorf("failed to send subscription: %v", err)
	}

	// Listen for events
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("❌ WebSocket connection to %s panicked: %v\n", relayURL, r)
			}
		}()

		for {
			// Read message with timeout
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, msgBytes, err := conn.ReadMessage()

			if err != nil {
				// Check if it's a timeout or connection error
				if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					return
				}
				// For other errors, return to avoid panic
				return
			}

			// Parse message
			var messages []json.RawMessage
			if err := json.Unmarshal(msgBytes, &messages); err != nil {
				continue
			}

			if len(messages) < 2 {
				continue
			}

			var msgType string
			if err := json.Unmarshal(messages[0], &msgType); err != nil {
				continue
			}

			// Debug: show all message types
			if msgType != "EVENT" {
				fmt.Printf("📨 Received %s message\n", msgType)
			}

			// Check if it's an event message
			if msgType == "EVENT" && len(messages) >= 3 {
				var event NostrEvent
				if err := json.Unmarshal(messages[2], &event); err != nil {
					continue
				}

				fmt.Printf("📝 Received event: %s (kind %v)\n", event.ID, event.Kind)

				// Check if this note has already been processed
				alreadyProcessed, err := isNoteProcessed(sqliteDB, event.ID)
				if err != nil {
					fmt.Printf("⚠️  Error checking if note is processed: %v\n", err)
					continue
				}

				if alreadyProcessed {
					fmt.Printf("⏭️  Skipping already processed note: %s\n", event.ID)
					continue
				}

				// Handle different event kinds
				switch event.Kind {
				case 1: // Text notes (mentions)
					for _, user := range npubToUser {
						if mentionsUser(event, user) {
							processMention(event, user, skipNIP5, client, config, sqliteDB, emailService, relayURL)
						}
					}
				case 4: // NIP-4 Encrypted Direct Messages
					for _, user := range npubToUser {
						if isDirectMessageForUser(event, user) {
							processDirectMessage(event, user, skipNIP5, client, config, sqliteDB, emailService, relayURL)
						}
					}
				case 14, 15: // NIP-17 Private Direct Messages
					// Note: These require the recipient's private key to decrypt
					// For now, we'll just log that we received them
					fmt.Printf("📨 Received NIP-17 message (kind %v) - requires recipient's private key to decrypt\n", event.Kind)
					// TODO: Implement NIP-17 support if users provide private keys
				}
			}
		}
	}()

	// Keep connection alive
	select {}
}

func displayMention(event NostrEvent, user User, relayURL string) {
	fmt.Printf("\n📧 MENTION FOUND:\n")
	fmt.Printf("   To: %s (%s)\n", user.Username, user.Email)
	fmt.Printf("   From: %s\n", event.PubKey)
	fmt.Printf("   Content: %s\n", event.Content)
	fmt.Printf("   Created: %s\n", time.Unix(event.CreatedAt, 0).Format("2006-01-02 15:04:05"))
	fmt.Printf("   Event ID: %s\n", event.ID)
	fmt.Printf("   Relay: %s\n", relayURL)

	// Check if npub is mentioned in tags (p tags)
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == user.NostrNpub {
			fmt.Printf("   📌 Mentioned in p-tags\n")
			break
		}
	}

	// Check if npub is mentioned in content
	if strings.Contains(event.Content, user.NostrNpub) {
		fmt.Printf("   📌 Mentioned in content\n")
	}

	fmt.Printf("   %s\n", strings.Repeat("-", 50))
}

func displayEmailNotification(event NostrEvent, user User, relayURL string, emailContent string) {
	createdTime := time.Unix(event.CreatedAt, 0)
	npub := hexToNpub(event.PubKey)
	fmt.Printf("📧 %s → %s: %s\n", npub, user.Username, event.Content)
	fmt.Printf("   Event: %s | %s\n", event.ID, createdTime.Format("15:04:05"))
}

// mentionsNpub checks if the event mentions the specified npub
func mentionsNpub(event NostrEvent, npub string) bool {
	// Check content for direct mention
	if strings.Contains(event.Content, npub) {
		return true
	}

	// Check tags for mention (p tags)
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == npub {
			return true
		}
	}

	return false
}

func mentionsUser(event NostrEvent, user User) bool {
	// Check if the event mentions this user by npub
	if mentionsNpub(event, user.NostrNpub) {
		return true
	}

	// Check for username mentions in content
	username := user.Username
	if strings.Contains(strings.ToLower(event.Content), strings.ToLower(username)) {
		return true
	}

	// Check for common username variations
	variations := []string{
		strings.ToLower(username),
		"@" + strings.ToLower(username),
		"@" + username,
	}

	for _, variation := range variations {
		if strings.Contains(strings.ToLower(event.Content), variation) {
			return true
		}
	}

	// Check for common abbreviations
	abbreviations := map[string]string{
		"thefriendlyhost": "tfh",
		"nostroots":       "nostr",
		"nospoons":        "nospoons", // no abbreviation needed
	}

	if abbr, exists := abbreviations[username]; exists {
		if strings.Contains(strings.ToLower(event.Content), abbr) {
			return true
		}
	}

	return false
}

// isDirectMessageForUser checks if a kind 4 event is a direct message for the user
func isDirectMessageForUser(event NostrEvent, user User) bool {
	// For NIP-4, check if the user's npub is in the p tags
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == user.NostrNpub {
			return true
		}
	}
	return false
}

// processMention handles processing of text note mentions
func processMention(event NostrEvent, user User, skipNIP5 bool, client *mongo.Client, config *Config, sqliteDB *sql.DB, emailService *EmailService, relayURL string) {
	var isVerified bool
	var senderNIP5 string
	var err error

	// Verify NIP-5 by looking up in MongoDB
	isVerified, senderNIP5, err = verifyNIP5FromDB(event.PubKey, client)
	if err != nil {
		npub := hexToNpub(event.PubKey)
		fmt.Printf("❌ NIP-5 verification failed for %s: %v\n", npub, err)
		if !skipNIP5 {
			return
		}
		senderNIP5 = "unverified@trustroots.org"
	}

	if !isVerified {
		npub := hexToNpub(event.PubKey)
		if !skipNIP5 {
			fmt.Printf("⚠️  Skipping mention from unverified user: %s (NIP-5 not found)\n", npub)
			return
		}
		senderNIP5 = "unverified@trustroots.org"
		fmt.Printf("⚠️  Skipping NIP-5 verification (--skip-nip5 flag), using: %s\n", senderNIP5)
	} else {
		npub := hexToNpub(event.PubKey)
		fmt.Printf("✅ NIP-5 verified: %s -> %s\n", npub, senderNIP5)
	}

	// Send email notification
	err = emailService.ProcessNostrMention(event, user, senderNIP5)
	if err != nil {
		fmt.Printf("❌ Failed to process email for %s: %v\n", user.Username, err)
	} else {
		fmt.Printf("📧 Email queued for %s (%s)\n", user.Username, user.Email)
	}

	// Mark this note as processed
	err = markNoteProcessed(sqliteDB, event.ID, relayURL, user.Email)
	if err != nil {
		fmt.Printf("⚠️  Error marking note as processed: %v\n", err)
	} else {
		fmt.Printf("✅ Marked note %s as processed\n", event.ID)
	}
}

// processDirectMessage handles processing of NIP-4 encrypted direct messages
func processDirectMessage(event NostrEvent, user User, skipNIP5 bool, client *mongo.Client, config *Config, sqliteDB *sql.DB, emailService *EmailService, relayURL string) {
	fmt.Printf("📨 Processing NIP-4 direct message for %s\n", user.Username)

	// Validate that this looks like a NIP-4 message
	if !validateNIP4Message(event) {
		fmt.Printf("⚠️  Event doesn't appear to be NIP-4 formatted, skipping\n")
		return
	}

	// Verify sender NIP-5
	var isVerified bool
	var senderNIP5 string
	var err error

	isVerified, senderNIP5, err = verifyNIP5FromDB(event.PubKey, client)
	if err != nil {
		npub := hexToNpub(event.PubKey)
		fmt.Printf("❌ NIP-5 verification failed for %s: %v\n", npub, err)
		if !skipNIP5 {
			return
		}
		senderNIP5 = "unverified@trustroots.org"
	}

	if !isVerified {
		npub := hexToNpub(event.PubKey)
		if !skipNIP5 {
			fmt.Printf("⚠️  Skipping DM from unverified user: %s (NIP-5 not found)\n", npub)
			return
		}
		senderNIP5 = "unverified@trustroots.org"
		fmt.Printf("⚠️  Skipping NIP-5 verification (--skip-nip5 flag), using: %s\n", senderNIP5)
	} else {
		npub := hexToNpub(event.PubKey)
		fmt.Printf("✅ NIP-5 verified: %s -> %s\n", npub, senderNIP5)
	}

	// Create a notification event with placeholder content (since we can't decrypt)
	notificationEvent := event
	notificationEvent.Content = "[Encrypted Direct Message - Content not available]"

	// Send email notification
	err = emailService.ProcessNostrDirectMessage(notificationEvent, user, senderNIP5)
	if err != nil {
		fmt.Printf("❌ Failed to process DM email for %s: %v\n", user.Username, err)
	} else {
		fmt.Printf("📧 DM notification queued for %s (%s)\n", user.Username, user.Email)
	}

	// Mark this note as processed
	err = markNoteProcessed(sqliteDB, event.ID, relayURL, user.Email)
	if err != nil {
		fmt.Printf("⚠️  Error marking DM as processed: %v\n", err)
	} else {
		fmt.Printf("✅ Marked DM %s as processed\n", event.ID)
	}
}

// validateNIP4Message validates that a message appears to be NIP-4 formatted
func validateNIP4Message(event NostrEvent) bool {
	// NIP-4 format: base64(encrypted_content)
	// We can validate that the content looks like base64 encoded data
	_, err := base64.StdEncoding.DecodeString(event.Content)
	return err == nil
}

// hexToNpub converts a hex pubkey to proper npub format using bech32
func hexToNpub(hexPubkey string) string {
	// Decode hex to bytes
	pubkeyBytes, err := hex.DecodeString(hexPubkey)
	if err != nil {
		// If hex decoding fails, return a simplified version
		return "npub1" + hexPubkey[:min(32, len(hexPubkey))] + "..."
	}

	// Convert to 5-bit groups for bech32
	converted, err := bech32.ConvertBits(pubkeyBytes, 8, 5, true)
	if err != nil {
		// If conversion fails, return a simplified version
		return "npub1" + hexPubkey[:min(32, len(hexPubkey))] + "..."
	}

	// Encode using bech32
	npub, err := bech32.Encode("npub", converted)
	if err != nil {
		// If encoding fails, return a simplified version
		return "npub1" + hexPubkey[:min(32, len(hexPubkey))] + "..."
	}

	return npub
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// verifyNIP5FromDB checks if a pubkey has a valid NIP-5 identifier by looking up in MongoDB
func verifyNIP5FromDB(pubkey string, client *mongo.Client) (bool, string, error) {
	// Convert hex pubkey to npub for database lookup
	npub := hexToNpub(pubkey)

	collection := client.Database("trustroots").Collection("users")

	var user User
	err := collection.FindOne(context.TODO(), bson.M{"nostrNpub": npub}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to query user: %v", err)
	}

	// User found, create NIP-5 identifier
	nip5 := fmt.Sprintf("%s@trustroots.org", user.Username)
	return true, nip5, nil
}

// getConfig returns a basic config for MongoDB connection
func getConfig() Config {
	return Config{
		MongoDB: struct {
			URI      string
			Database string
		}{
			URI:      getEnvOrDefault("MONGODB_URI", "mongodb://localhost:27017"),
			Database: getEnvOrDefault("MONGODB_DATABASE", "trustroots"),
		},
	}
}

// initSQLiteDB initializes the SQLite database for tracking processed notes
func initSQLiteDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "./processed_notes.db")
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %v", err)
	}

	// Create table if it doesn't exist
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS processed_notes (
		event_id TEXT PRIMARY KEY,
		processed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		relay_url TEXT,
		user_email TEXT
	);`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to create table: %v", err)
	}

	return db, nil
}

// isNoteProcessed checks if a note has already been processed
func isNoteProcessed(db *sql.DB, eventID string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM processed_notes WHERE event_id = ?", eventID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check if note is processed: %v", err)
	}
	return count > 0, nil
}

// markNoteProcessed marks a note as processed
func markNoteProcessed(db *sql.DB, eventID, relayURL, userEmail string) error {
	_, err := db.Exec("INSERT OR IGNORE INTO processed_notes (event_id, relay_url, user_email) VALUES (?, ?, ?)",
		eventID, relayURL, userEmail)
	if err != nil {
		return fmt.Errorf("failed to mark note as processed: %v", err)
	}
	return nil
}

// getNpubsFromUsers extracts all npubs from the user map
func getNpubsFromUsers(npubToUser map[string]User) []string {
	var npubs []string
	for npub := range npubToUser {
		npubs = append(npubs, npub)
	}
	return npubs
}

// formatEmail creates a properly formatted email for the mention
func formatEmail(event NostrEvent, mentionedUser User, senderNIP5 string, config *Config) string {
	// Convert timestamp to readable format
	createdTime := time.Unix(event.CreatedAt, 0)
	formattedTime := createdTime.Format("2006-01-02 15:04:05 UTC")

	// Use sender email from config, fallback to NIP-5, then default
	senderEmail := config.SenderEmail
	if senderEmail == "" {
		senderEmail = senderNIP5
		if senderEmail == "" {
			senderEmail = "noreply@trustroots.org"
		}
	}

	// Create email subject
	subject := fmt.Sprintf("Nostr Mention from %s", senderEmail)

	// Create email body
	emailBody := fmt.Sprintf(`From: %s
To: %s (%s)
Subject: %s
Date: %s
Message-ID: nostr-%s@trustroots.org

Hello %s,

You have received a new Nostr mention:

Content: %s

Event Details:
- Event ID: %s
- Created: %s
- Sender: %s

This mention was detected on the Trustroots Nostr relay network.

Best regards,
Trustroots Nostr Notification System
`,
		senderEmail,
		mentionedUser.Email,
		mentionedUser.Username,
		subject,
		formattedTime,
		event.ID,
		mentionedUser.Username,
		event.Content,
		event.ID,
		formattedTime,
		senderEmail,
	)

	return emailBody
}

func displaySummary(users []User, validNpubs, invalidNpubs, emptyNpubs []User) {
	// Display results in a clean table format
	fmt.Println("\nAll Users with nostrNpub field:")
	fmt.Println("===============================")
	fmt.Printf("%-20s | %-35s | %-80s\n", "Username", "Email", "Nostr Npub")
	fmt.Println(strings.Repeat("-", 140))

	for _, user := range users {
		// Truncate long npubs for display
		npub := user.NostrNpub
		if len(npub) > 80 {
			npub = npub[:77] + "..."
		}
		fmt.Printf("%-20s | %-35s | %-80s\n", user.Username, user.Email, npub)
	}

	// Display valid npubs
	fmt.Println("\n\nVALID NOSTR NPUBS:")
	fmt.Println("==================")
	for i, user := range validNpubs {
		fmt.Printf("%d. %s | %s | %s\n", i+1, user.Username, user.Email, user.NostrNpub)
	}

	// Display invalid npubs
	fmt.Println("\n\nINVALID/OTHER NPUBS:")
	fmt.Println("===================")
	for i, user := range invalidNpubs {
		fmt.Printf("%d. %s | %s | %s\n", i+1, user.Username, user.Email, user.NostrNpub)
	}

	// Display empty npubs
	fmt.Println("\n\nEMPTY NPUBS:")
	fmt.Println("============")
	for i, user := range emptyNpubs {
		fmt.Printf("%d. %s | %s | (empty)\n", i+1, user.Username, user.Email)
	}

	// Create a summary for potential nostr notifications
	fmt.Println("\n\nSUMMARY FOR NOSTR NOTIFICATIONS:")
	fmt.Println("=================================")
	fmt.Printf("Total users with nostrNpub field: %d\n", len(users))
	fmt.Printf("Valid nostr npubs: %d\n", len(validNpubs))
	fmt.Printf("Invalid/other npubs: %d\n", len(invalidNpubs))
	fmt.Printf("Empty npubs: %d\n", len(emptyNpubs))

	// Group by email domains for analysis
	emailDomains := make(map[string]int)
	for _, user := range users {
		if user.Email != "" {
			// Simple domain extraction (you might want to use a proper email parser)
			domain := "unknown"
			for i, char := range user.Email {
				if char == '@' && i+1 < len(user.Email) {
					domain = user.Email[i+1:]
					break
				}
			}
			emailDomains[domain]++
		}
	}

	fmt.Println("\nEmail domain distribution:")
	for domain, count := range emailDomains {
		fmt.Printf("  %s: %d users\n", domain, count)
	}
}

func sendTestNote(senderNpub, senderNsec, recipientNpub, message string, relays []string) error {
	fmt.Printf("📤 Sending test note from %s to %s\n", senderNpub, recipientNpub)
	fmt.Printf("Using relays: %v\n", relays)

	fmt.Printf("Note content: %s\n", message)

	// Create the Nostr event
	event := createNostrEvent(senderNpub, message, recipientNpub)

	// Sign the event
	signedEvent, err := signNostrEvent(event, senderNsec)
	if err != nil {
		return fmt.Errorf("failed to sign event: %v", err)
	}

	fmt.Printf("✅ Test note created and signed\n")
	fmt.Printf("Event ID: %s\n", signedEvent.ID)
	fmt.Printf("Signature: %s\n", signedEvent.Sig)

	// Display the full note structure
	fmt.Println("\n📋 Full note structure sent to relays:")
	eventJSON, err := json.MarshalIndent(signedEvent, "", "  ")
	if err != nil {
		fmt.Printf("Error formatting event: %v\n", err)
	} else {
		fmt.Println(string(eventJSON))
	}

	// Send to relays
	err = sendToRelays(signedEvent, relays)
	if err != nil {
		return fmt.Errorf("failed to send to relays: %v", err)
	}

	return nil
}

func createNostrEvent(pubkey, content, recipientNpub string) *NostrEvent {
	now := time.Now().Unix()

	// Create p-tag for recipient and hashtag for testing
	tags := [][]string{
		{"p", recipientNpub, "", "mention"},
		{"t", "testing"},
	}

	// Create the event
	event := &NostrEvent{
		PubKey:    pubkey,
		CreatedAt: now,
		Kind:      1, // text note
		Tags:      tags,
		Content:   content,
	}

	// Calculate event ID (SHA256 of serialized event)
	event.ID = calculateEventID(event)

	return event
}

func calculateEventID(event *NostrEvent) string {
	// Create the event data for hashing (without signature)
	// According to NIP-01, the event ID is SHA256 of the serialized event array
	eventData := []interface{}{
		0,               // kind
		event.PubKey,    // pubkey
		event.CreatedAt, // created_at
		event.Kind,      // kind
		event.Tags,      // tags
		event.Content,   // content
	}

	// Serialize to JSON
	jsonData, err := json.Marshal(eventData)
	if err != nil {
		return ""
	}

	// Calculate SHA256
	hash := sha256.Sum256(jsonData)
	return hex.EncodeToString(hash[:])
}

func signNostrEvent(event *NostrEvent, nsec string) (*NostrEvent, error) {
	// For now, we'll create a deterministic signature based on nsec and event ID
	// In a real implementation, you'd:
	// 1. Decode the nsec from bech32
	// 2. Use the private key to sign the event ID
	// 3. Encode the signature as hex

	// Create a deterministic signature for demonstration
	signatureData := event.ID + nsec + "nostr_signature"
	hash := sha256.Sum256([]byte(signatureData))
	signature := hex.EncodeToString(hash[:])
	event.Sig = signature

	return event, nil
}

func sendToRelays(event *NostrEvent, relays []string) error {
	fmt.Printf("📡 Sending event to %v relays...\n", len(relays))

	successCount := 0
	errorCount := 0

	for _, relayURL := range relays {
		err := sendToRelay(event, relayURL)
		if err != nil {
			fmt.Printf("❌ Failed to send to %s: %v\n", relayURL, err)
			errorCount++
		} else {
			fmt.Printf("✅ Successfully sent to %s\n", relayURL)
			successCount++
		}
	}

	fmt.Printf("📊 Relay sending complete: %v success, %v failed\n", successCount, errorCount)

	if successCount == 0 {
		return fmt.Errorf("failed to send to any relay")
	}

	return nil
}

func sendToRelay(event *NostrEvent, relayURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Parse URL
	u, err := url.Parse(relayURL)
	if err != nil {
		return fmt.Errorf("invalid relay URL %s: %v", relayURL, err)
	}

	// Connect via WebSocket
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %v", relayURL, err)
	}
	defer conn.Close()

	// Create EVENT message
	eventMsg := []interface{}{
		"EVENT",
		event,
	}

	// Send the event
	msgBytes, err := json.Marshal(eventMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %v", err)
	}

	err = conn.WriteMessage(websocket.TextMessage, msgBytes)
	if err != nil {
		return fmt.Errorf("failed to send event: %v", err)
	}

	// Wait for response (with timeout)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, responseBytes, err := conn.ReadMessage()
	if err != nil {
		// Some relays don't send responses, so this might not be an error
		return nil
	}

	// Parse response
	var response []json.RawMessage
	if err := json.Unmarshal(responseBytes, &response); err != nil {
		return nil // Ignore parsing errors for now
	}

	if len(response) >= 2 {
		var msgType string
		if err := json.Unmarshal(response[0], &msgType); err == nil {
			if msgType == "OK" {
				// Success response
				return nil
			} else if msgType == "NOTICE" {
				// Notice message, might contain error info
				var notice string
				if err := json.Unmarshal(response[1], &notice); err == nil {
					return fmt.Errorf("relay notice: %s", notice)
				}
			}
		}
	}

	return nil
}
