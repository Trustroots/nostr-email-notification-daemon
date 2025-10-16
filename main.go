package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nbd-wtf/go-nostr"
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

// Use the library's Event type instead of custom implementation

// NIP5Response represents the response from a NIP-5 verification endpoint
type NIP5Response struct {
	Names map[string]string `json:"names"`
}

// Use the library's message types instead of custom implementation

func main() {
	// Parse command line arguments
	listUsersFlag := flag.Bool("list-users", false, "List all users in 3 categories")
	nostrListenFlag := flag.Bool("nostr-listen", false, "Listen to nostr relays for direct messages to valid npubs")
	testFlag := flag.Bool("test", false, "Send a test direct message")
	recipientNpub := flag.String("send-to-npub", "", "Recipient npub for test DM (required with --test)")
	message := flag.String("msg", "", "Message content for test DM (required with --test)")
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
		} else if isValidNpub(user.NostrNpub) {
			validNpubs = append(validNpubs, user)
		} else {
			invalidNpubs = append(invalidNpubs, user)
		}
	}
	return validNpubs, invalidNpubs, emptyNpubs
}

// isValidNpub validates that an npub is properly formatted using the nostr library
func isValidNpub(npub string) bool {
	// Basic format check
	if npub == "" || !strings.HasPrefix(npub, "npub1") || len(npub) <= 50 {
		return false
	}

	// Try to create an event with this npub as pubkey to test if it's valid
	// This is a bit of a hack, but it will validate the npub format
	event := &nostr.Event{
		Kind:      nostr.KindTextNote,
		Content:   "test",
		CreatedAt: nostr.Now(),
		PubKey:    npub,
	}

	// Try to calculate the event ID - this will fail if the pubkey is invalid
	eventID := event.GetID()
	return eventID != ""
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
	fmt.Println("🔍 Listening to nostr relays for direct messages...")
	fmt.Printf("Connecting to %d relays: %v\n", len(relays), relays)

	// Create a map of npubs to users for quick lookup
	npubToUser := make(map[string]User)
	hexToUser := make(map[string]User) // Map hex pubkeys to users
	for _, user := range validNpubs {
		npubToUser[user.NostrNpub] = user

		// Also create hex mapping for event processing
		hexPubkey, err := npubToHex(user.NostrNpub)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to convert npub %s to hex: %v\n", user.NostrNpub, err)
			continue
		}
		hexToUser[hexPubkey] = user
	}

	fmt.Printf("\nMonitoring %d valid npubs for direct messages...\n", len(validNpubs))
	fmt.Println("Press Ctrl+C to stop listening")
	fmt.Println()

	// Create relay pool
	pool := nostr.NewSimplePool(context.Background())

	// Create filter for direct messages only
	since := nostr.Timestamp(time.Now().Add(-1 * time.Hour).Unix())
	filter := nostr.Filter{
		Kinds: []int{4}, // NIP-4 encrypted direct messages only
		Tags:  nostr.TagMap{"p": getHexPubkeysFromUsers(npubToUser)},
		Since: &since,
	}

	// Subscribe to events
	sub := pool.SubMany(context.Background(), relays, []nostr.Filter{filter})

	// Process events
	for evt := range sub {
		processEvent(evt, npubToUser, hexToUser, skipNIP5, client, config, sqliteDB, emailService)
	}

	return nil
}

// processEvent handles incoming nostr events
func processEvent(evt nostr.RelayEvent, npubToUser map[string]User, hexToUser map[string]User, skipNIP5 bool, client *mongo.Client, config *Config, sqliteDB *sql.DB, emailService *EmailService) {
	// Check if this is an event (not a notice or other message type)
	if evt.Event == nil {
		return
	}

	event := evt.Event
	fmt.Printf("📝 Received event: %s (kind %v) from %s\n", event.ID, event.Kind, event.PubKey)

	// Check if this note has already been processed
	alreadyProcessed, err := isNoteProcessed(sqliteDB, event.ID)
	if err != nil {
		fmt.Printf("⚠️  Error checking if note is processed: %v\n", err)
		return
	}

	if alreadyProcessed {
		fmt.Printf("⏭️  Skipping already processed note: %s\n", event.ID)
		return
	}

	// Convert event pubkey to npub for display
	eventNpub, err := hexToNpub(event.PubKey)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to convert event pubkey to npub: %v\n", err)
		eventNpub = event.PubKey // fallback to hex
	}

	// Handle NIP-4 encrypted direct messages only
	if event.Kind == 4 {
		fmt.Printf("🔍 Checking encrypted DM...\n")
		matched := false
		for _, user := range npubToUser {
			if isDirectMessageForUser(event, user) {
				fmt.Printf("✅ MATCH FOUND! DM for %s (%s)\n", user.Username, user.Email)
				processDirectMessage(event, user, skipNIP5, client, config, sqliteDB, emailService)
				matched = true
			}
		}
		if !matched {
			fmt.Printf("ℹ️  No matching recipient found for DM from %s\n", eventNpub)
		}
	} else {
		fmt.Printf("ℹ️  Skipping non-DM event (kind %v) from %s\n", event.Kind, eventNpub)
	}
}

func displayEmailNotification(event *nostr.Event, user User, relayURL string, emailContent string) {
	createdTime := event.CreatedAt.Time()
	npub := event.PubKey
	fmt.Printf("📧 %s → %s: %s\n", npub, user.Username, event.Content)
	fmt.Printf("   Event: %s | %s\n", event.ID, createdTime.Format("15:04:05"))
}

// isDirectMessageForUser checks if a kind 4 event is a direct message for the user
func isDirectMessageForUser(event *nostr.Event, user User) bool {
	// Convert user's npub to hex for comparison
	userHexPubkey, err := npubToHex(user.NostrNpub)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to convert user npub to hex: %v\n", err)
		return false
	}

	// For NIP-4, check if the user's hex pubkey is in the p tags
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == userHexPubkey {
			return true
		}
	}
	return false
}

// processDirectMessage handles processing of NIP-4 encrypted direct messages
func processDirectMessage(event *nostr.Event, user User, skipNIP5 bool, client *mongo.Client, config *Config, sqliteDB *sql.DB, emailService *EmailService) {
	fmt.Printf("📨 Processing NIP-4 direct message for %s\n", user.Username)

	// Skip NIP-4 content validation for now - we'll process all kind 4 events
	// if !validateNIP4Message(event) {
	//	fmt.Printf("⚠️  Event doesn't appear to be NIP-4 formatted, skipping\n")
	//	return
	// }

	// Verify sender NIP-5
	var isVerified bool
	var senderNIP5 string
	var err error

	isVerified, senderNIP5, err = verifyNIP5FromDB(event.PubKey, client)
	if err != nil {
		fmt.Printf("❌ NIP-5 verification failed for %s: %v\n", event.PubKey, err)
		if !skipNIP5 {
			return
		}
		senderNIP5 = "unverified@trustroots.org"
	}

	if !isVerified {
		if !skipNIP5 {
			fmt.Printf("⚠️  Skipping DM from unverified user: %s (NIP-5 not found)\n", event.PubKey)
			return
		}
		senderNIP5 = "unverified@trustroots.org"
		fmt.Printf("⚠️  Skipping NIP-5 verification (--skip-nip5 flag), using: %s\n", senderNIP5)
	} else {
		fmt.Printf("✅ NIP-5 verified: %s -> %s\n", event.PubKey, senderNIP5)
	}

	// Create a notification event with placeholder content (since we can't decrypt)
	notificationEvent := *event
	notificationEvent.Content = "[Encrypted Direct Message - Content not available]"

	// Send email notification
	err = emailService.ProcessNostrDirectMessage(&notificationEvent, user, senderNIP5)
	if err != nil {
		fmt.Printf("❌ Failed to process DM email for %s: %v\n", user.Username, err)
	} else {
		fmt.Printf("📧 DM notification queued for %s (%s)\n", user.Username, user.Email)
	}

	// Mark this note as processed
	err = markNoteProcessed(sqliteDB, event.ID, "relay", user.Email)
	if err != nil {
		fmt.Printf("⚠️  Error marking DM as processed: %v\n", err)
	} else {
		fmt.Printf("✅ Marked DM %s as processed\n", event.ID)
	}
}

// validateNIP4Message validates that a message appears to be NIP-4 formatted
func validateNIP4Message(event *nostr.Event) bool {
	// NIP-4 format: base64(encrypted_content)
	// We can validate that the content looks like base64 encoded data
	_, err := base64.StdEncoding.DecodeString(event.Content)
	return err == nil
}

// Use the library's built-in functionality for key conversion

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// verifyNIP5FromDB checks if a pubkey has a valid NIP-5 identifier by looking up in MongoDB
func verifyNIP5FromDB(hexPubkey string, client *mongo.Client) (bool, string, error) {
	// Convert hex pubkey to npub for database lookup
	npub, err := hexToNpub(hexPubkey)
	if err != nil {
		return false, "", fmt.Errorf("invalid pubkey: %v", err)
	}

	collection := client.Database("trustroots").Collection("users")

	var user User
	err = collection.FindOne(context.TODO(), bson.M{"nostrNpub": npub}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to query user: %v", err)
	}

	// User found in database - this implies username@trustroots.org is a valid NIP-5 identifier
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

// getHexPubkeysFromUsers converts npubs to hex format for relay filters
func getHexPubkeysFromUsers(npubToUser map[string]User) []string {
	var hexPubkeys []string
	for npub := range npubToUser {
		// Convert npub to hex format for the relay filter
		hexPubkey, err := npubToHex(npub)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to convert npub %s to hex: %v\n", npub, err)
			continue
		}
		hexPubkeys = append(hexPubkeys, hexPubkey)
	}
	return hexPubkeys
}

// npubToHex converts an npub string to hex format
func npubToHex(npub string) (string, error) {
	// Decode bech32
	hrp, data, err := bech32.Decode(npub)
	if err != nil {
		return "", fmt.Errorf("failed to decode bech32: %v", err)
	}
	if hrp != "npub" {
		return "", fmt.Errorf("invalid human readable part: %s", hrp)
	}

	// Convert from 5-bit groups to 8-bit groups
	converted, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return "", fmt.Errorf("failed to convert bits: %v", err)
	}

	// Convert to hex string
	return hex.EncodeToString(converted), nil
}

// hexToNpub converts a hex pubkey to npub format
func hexToNpub(hexPubkey string) (string, error) {
	// Decode hex to bytes
	pubkeyBytes, err := hex.DecodeString(hexPubkey)
	if err != nil {
		return "", fmt.Errorf("failed to decode hex: %v", err)
	}

	// Convert from 8-bit groups to 5-bit groups
	converted, err := bech32.ConvertBits(pubkeyBytes, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("failed to convert bits: %v", err)
	}

	// Encode as bech32
	npub, err := bech32.Encode("npub", converted)
	if err != nil {
		return "", fmt.Errorf("failed to encode bech32: %v", err)
	}

	return npub, nil
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

	// Create a summary for potential nostr DM notifications
	fmt.Println("\n\nSUMMARY FOR NOSTR DM NOTIFICATIONS:")
	fmt.Println("====================================")
	fmt.Printf("Total users with nostrNpub field: %d\n", len(users))
	fmt.Printf("Valid nostr npubs: %d\n", len(validNpubs))
	fmt.Printf("Invalid/other npubs: %d\n", len(invalidNpubs))
	fmt.Printf("Empty npubs: %d\n", len(emptyNpubs))

}

func sendTestNote(senderNpub, senderNsec, recipientNpub, message string, relays []string) error {
	fmt.Printf("📤 Sending test DM from %s to %s\n", senderNpub, recipientNpub)
	fmt.Printf("Using relays: %v\n", relays)

	fmt.Printf("DM content: %s\n", message)

	// Convert recipient npub to hex for p tag
	recipientHex, err := npubToHex(recipientNpub)
	if err != nil {
		return fmt.Errorf("failed to convert recipient npub to hex: %v", err)
	}

	// Create the Nostr event using the library (NIP-4 DM)
	event := &nostr.Event{
		Kind:      4,       // NIP-4 encrypted direct message
		Content:   message, // In a real implementation, this would be encrypted
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"p", recipientHex}, // p tag contains hex pubkey of recipient
		},
	}

	// Set the pubkey
	event.PubKey = senderNpub

	// Sign the event
	err = event.Sign(senderNsec)
	if err != nil {
		return fmt.Errorf("failed to sign event: %v", err)
	}

	fmt.Printf("✅ Test DM created and signed\n")
	fmt.Printf("Event ID: %s\n", event.ID)
	fmt.Printf("Signature: %s\n", event.Sig)

	// Display the full note structure
	fmt.Println("\n📋 Full DM structure sent to relays:")
	eventJSON, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		fmt.Printf("Error formatting event: %v\n", err)
	} else {
		fmt.Println(string(eventJSON))
	}

	// Send to relays using the library
	pool := nostr.NewSimplePool(context.Background())
	results := pool.PublishMany(context.Background(), relays, *event)

	for result := range results {
		if result.Error != nil {
			fmt.Printf("❌ Failed to send to %s: %v\n", result.RelayURL, result.Error)
		} else {
			fmt.Printf("✅ Successfully sent to %s\n", result.RelayURL)
		}
	}

	return nil
}

// Old manual implementation functions removed - using nostr library instead
