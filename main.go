package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
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

// Config represents the configuration file structure
type Config struct {
	MongoDB struct {
		URI      string `json:"uri"`
		Database string `json:"database"`
	} `json:"mongodb"`
	SenderNpub string   `json:"sender-npub"`
	SenderNsec string   `json:"sender-nsec"`
	Relays     []string `json:"relays"`
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
	flag.Parse()

	// Load configuration
	config, err := loadConfig("config.json")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Connect to MongoDB
	client, err := connectToMongoDB(config)
	if err != nil {
		log.Fatal("Failed to connect to MongoDB:", err)
	}
	defer func() {
		if err = client.Disconnect(context.TODO()); err != nil {
			log.Fatal("Failed to disconnect from MongoDB:", err)
		}
	}()

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
		err = listenToNostrRelays(validNpubs, config.Relays)
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
		err = sendTestNote(config.SenderNpub, *recipientNpub, *message, config.Relays)
		if err != nil {
			log.Fatal("Failed to send test note:", err)
		}
		return
	}

	// Default behavior - show summary
	displaySummary(users, validNpubs, invalidNpubs, emptyNpubs)
}

func loadConfig(filename string) (*Config, error) {
	file, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(file, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func connectToMongoDB(config *Config) (*mongo.Client, error) {
	clientOptions := options.Client().ApplyURI(config.MongoDB.URI)
	client, err := mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		return nil, err
	}

	// Ping the database to verify connection
	err = client.Ping(context.TODO(), nil)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Successfully connected to MongoDB at %s!\n", config.MongoDB.URI)
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

func listenToNostrRelays(validNpubs []User, relays []string) error {
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
			err := connectToRelay(relay, npubToUser)
			if err != nil {
				fmt.Printf("❌ Error connecting to %s: %v\n", relay, err)
			}
		}(relayURL)
	}

	// Keep the main thread alive
	select {}
}

func connectToRelay(relayURL string, npubToUser map[string]User) error {

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

	// Create a single subscription for all npubs
	subscribeMsg := []interface{}{
		"REQ",
		subID,
		map[string]interface{}{
			"kinds": []int{1},
			"limit": 10,
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
		timeout := time.After(30 * time.Second)

		for {
			select {
			case <-timeout:
				// Close this subscription
				closeMsg := []interface{}{"CLOSE", subID}
				closeBytes, _ := json.Marshal(closeMsg)
				conn.WriteMessage(websocket.TextMessage, closeBytes)
				return
			default:
				// Read message with timeout
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, msgBytes, err := conn.ReadMessage()

				if err != nil {
					// Check if it's a timeout or connection error
					if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
						return
					}
					// For other errors, continue to next iteration
					continue
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

				// Check if it's an event message
				if msgType == "EVENT" && len(messages) >= 3 {
					var event NostrEvent
					if err := json.Unmarshal(messages[2], &event); err != nil {
						continue
					}

					// Check if this event mentions any of our npubs
					for npub, user := range npubToUser {
						if mentionsNpub(event, npub) {
							displayMention(event, user, relayURL)
						}
					}
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

	fmt.Printf("   " + strings.Repeat("-", 50) + "\n")
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

func sendTestNote(senderNpub, recipientNpub, message string, relays []string) error {
	fmt.Printf("📤 Sending test note from %s to %s\n", senderNpub, recipientNpub)
	fmt.Printf("Using relays: %v\n", relays)

	fmt.Printf("Note content: %s\n", message)
	fmt.Println("✅ Test note created (signature not implemented yet)")

	return nil
}
