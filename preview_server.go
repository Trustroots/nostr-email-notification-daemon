package main

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/vanng822/go-premailer/premailer"
)

// Note: EmailTemplateData and EmailSender are defined in email_service.go

// Sample data for direct message preview
var sampleDMData = EmailTemplateData{
	// User data
	Username:  "alex_wanderer",
	Name:      "Alex Wanderer",
	FirstName: "Alex",
	Email:     "alex.wanderer@example.com",

	// URLs
	HeaderURL:        "https://trustroots.org",
	FooterURL:        "https://trustroots.org",
	SupportURL:       "https://trustroots.org/support",
	ProfileURL:       "https://www.trustroots.org/profile/alex_wanderer",
	SenderProfileURL: "https://www.trustroots.org/profile/maria_traveler",

	// Email content
	Subject:   "Encrypted DM from maria_traveler@trustroots.org",
	Title:     "New Encrypted Direct Message",
	MailTitle: "New Encrypted Direct Message",

	// Sender info
	From: EmailSender{
		Name:    "Trustroots Nostr",
		Address: "noreply@trustroots.org",
	},

	// Campaign tracking
	UTMCampaign:       "nostr-notification",
	SparkpostCampaign: "nostr-test",

	// Custom content
	Content: map[string]interface{}{
		"buttonURL":      "https://tripch.at/#dm:npub1maria123456789abcdefghijklmnopqrstuvwxyz",
		"buttonText":     "View on TRipch.at",
		"senderName":     "Maria Traveler",
		"messagePreview": "Hey Alex! I saw your profile and I'm planning to visit Barcelona next month. Would love to meet up for coffee and hear about your travels!",
		"senderLocation": "Madrid, Spain",
		"messageTime":    "2 hours ago",
	},

	// Nostr specific fields
	EventContent:   "[Encrypted Direct Message - Content not available]",
	EventID:        "sample-dm-event-id-67890abcdef123456789",
	CreatedAt:      time.Now().Format("2006-01-02 15:04:05 UTC"),
	SenderNIP5:     "maria_traveler@trustroots.org",
	SenderUsername: "maria_traveler",
	SenderNpub:     "npub1maria123456789abcdefghijklmnopqrstuvwxyz",
	RecipientNpub:  "npub1alex123456789abcdefghijklmnopqrstuvwxyz",
}

// Sample data for digest email preview
var sampleDigestData = EmailTemplateData{
	// User data
	Username:  "sarah_explorer",
	Name:      "Sarah Explorer",
	FirstName: "Sarah",
	Email:     "sarah.explorer@example.com",

	// URLs
	HeaderURL:        "https://trustroots.org",
	FooterURL:        "https://trustroots.org",
	SupportURL:       "https://trustroots.org/support",
	ProfileURL:       "https://www.trustroots.org/profile/sarah_explorer",
	SenderProfileURL: "https://www.trustroots.org/profile/nostroots",

	// Email content
	Subject:   "You have 5 new encrypted messages",
	Title:     "New Encrypted Messages Digest",
	MailTitle: "New Encrypted Messages Digest",

	// Sender info
	From: EmailSender{
		Name:    "Trustroots Nostr",
		Address: "noreply@trustroots.org",
	},

	// Campaign tracking
	UTMCampaign:       "nostr-digest",
	SparkpostCampaign: "nostr-digest-test",

	// Custom content for digest
	Content: map[string]interface{}{
		"messageCount": 5,
		"buttonURL":    "https://tripch.at/#dm:npub1sarah123456789abcdefghijklmnopqrstuvwxyz",
		"buttonText":   "View Messages on TRipch.at",
		"senders": []map[string]interface{}{
			{"name": "Carlos Backpacker", "location": "Mexico City", "time": "3 hours ago"},
			{"name": "Emma Nomad", "location": "Berlin", "time": "5 hours ago"},
			{"name": "James Wanderer", "location": "Tokyo", "time": "1 day ago"},
			{"name": "Lisa Explorer", "location": "Bangkok", "time": "1 day ago"},
			{"name": "Mike Traveler", "location": "Sydney", "time": "2 days ago"},
		},
		"timeRange": "last 2 days",
	},

	// Nostr specific fields
	EventContent:   "[Digest of 5 encrypted messages]",
	EventID:        "sample-digest-event-id-12345abcdef67890",
	CreatedAt:      time.Now().Format("2006-01-02 15:04:05 UTC"),
	SenderNIP5:     "nostroots@trustroots.org",
	SenderUsername: "nostroots",
	SenderNpub:     "npub1sarah123456789abcdefghijklmnopqrstuvwxyz",
	RecipientNpub:  "npub1sarah123456789abcdefghijklmnopqrstuvwxyz",
}

// renderHTMLTemplate renders the HTML email template
func renderHTMLTemplate(templateName string, data EmailTemplateData) (string, error) {
	// Load HTML templates
	htmlTemplates, err := template.ParseGlob("templates/html/*.html")
	if err != nil {
		return "", fmt.Errorf("failed to load HTML templates: %v", err)
	}

	var buf bytes.Buffer
	if err := htmlTemplates.ExecuteTemplate(&buf, templateName+".html", data); err != nil {
		return "", fmt.Errorf("failed to execute HTML template %s: %v", templateName, err)
	}

	// Inline CSS for better email client compatibility
	prem, err := premailer.NewPremailerFromString(buf.String(), premailer.NewOptions())
	if err != nil {
		return "", fmt.Errorf("failed to create premailer: %v", err)
	}

	html, err := prem.Transform()
	if err != nil {
		return "", fmt.Errorf("failed to transform HTML: %v", err)
	}

	return html, nil
}

// renderTextTemplate renders the plain text email template
func renderTextTemplate(templateName string, data EmailTemplateData) (string, error) {
	// Load text templates
	textTemplates, err := template.ParseGlob("templates/text/*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to load text templates: %v", err)
	}

	var buf bytes.Buffer
	if err := textTemplates.ExecuteTemplate(&buf, templateName+".txt", data); err != nil {
		return "", fmt.Errorf("failed to execute text template %s: %v", templateName, err)
	}

	return buf.String(), nil
}

// handleDMPreview renders the direct message email preview
func handleDMPreview(w http.ResponseWriter, r *http.Request) {
	html, err := renderHTMLTemplate("nostr_direct_message", sampleDMData)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error rendering template: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

// handleTextDMPreview renders the direct message text email preview
func handleTextDMPreview(w http.ResponseWriter, r *http.Request) {
	text, err := renderTextTemplate("nostr_direct_message", sampleDMData)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error rendering template: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, text)
}

// handleDigestPreview renders the digest email preview
func handleDigestPreview(w http.ResponseWriter, r *http.Request) {
	html, err := renderHTMLTemplate("digest", sampleDigestData)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error rendering template: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

// handleTextDigestPreview renders the digest text email preview
func handleTextDigestPreview(w http.ResponseWriter, r *http.Request) {
	text, err := renderTextTemplate("digest", sampleDigestData)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error rendering template: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, text)
}

// handleIndex renders the main index page with links to all previews
func handleIndex(w http.ResponseWriter, r *http.Request) {
	html := `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Nostr Email Preview</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; background-color: #f5f5f5; }
        .container { max-width: 800px; margin: 0 auto; background: white; padding: 30px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #12b591; }
        .preview-section { margin: 20px 0; padding: 20px; border: 1px solid #ddd; border-radius: 5px; }
        .preview-section h2 { margin-top: 0; color: #333; }
        .preview-links { display: flex; gap: 10px; flex-wrap: wrap; }
        .preview-links a { 
            display: inline-block; 
            padding: 10px 20px; 
            background-color: #12b591; 
            color: white; 
            text-decoration: none; 
            border-radius: 4px; 
            transition: background-color 0.3s;
        }
        .preview-links a:hover { background-color: #0fa078; }
        .description { color: #666; margin-bottom: 15px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Trustroots Nostr Email Preview</h1>
        <p>Preview how the email notifications will look to users.</p>
        
        <div class="preview-section">
            <h2>Direct Message Notifications</h2>
            <div class="description">When someone sends an encrypted direct message</div>
            <div class="preview-links">
                <a href="/preview/dm/html" target="_blank">HTML Preview</a>
                <a href="/preview/dm/text" target="_blank">Text Preview</a>
            </div>
        </div>
        
        <div class="preview-section">
            <h2>Digest Notifications</h2>
            <div class="description">When multiple encrypted messages are received (digest format)</div>
            <div class="preview-links">
                <a href="/preview/digest/html" target="_blank">HTML Preview</a>
                <a href="/preview/digest/text" target="_blank">Text Preview</a>
            </div>
        </div>
        
        <div class="preview-section">
            <h2>Sample Data</h2>
            <div class="description">Current sample data being used for previews:</div>
            <ul>
                <li><strong>Direct Message Recipient:</strong> alex.wanderer@example.com (alex_wanderer)</li>
                <li><strong>Direct Message Sender:</strong> maria_traveler@trustroots.org</li>
                <li><strong>Digest Recipient:</strong> sarah.explorer@example.com (sarah_explorer)</li>
                <li><strong>Direct Message Event ID:</strong> sample-dm-event-id-67890abcdef123456789</li>
                <li><strong>Digest Event ID:</strong> sample-digest-event-id-12345abcdef67890</li>
                <li><strong>Digest Message Count:</strong> 5 messages from 5 different senders</li>
                <li><strong>Profile URLs:</strong> Dynamic based on usernames</li>
            </ul>
        </div>
    </div>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

func startPreviewServer() {
	// Set up routes
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/preview/dm/html", handleDMPreview)
	http.HandleFunc("/preview/dm/text", handleTextDMPreview)
	http.HandleFunc("/preview/digest/html", handleDigestPreview)
	http.HandleFunc("/preview/digest/text", handleTextDigestPreview)

	// Start server
	port := "8080"
	fmt.Printf("🚀 Email preview server starting on http://localhost:%s\n", port)
	fmt.Println("📧 Available previews:")
	fmt.Println("   • HTML Direct Message: http://localhost:8080/preview/dm/html")
	fmt.Println("   • Text Direct Message: http://localhost:8080/preview/dm/text")
	fmt.Println("   • HTML Digest: http://localhost:8080/preview/digest/html")
	fmt.Println("   • Text Digest: http://localhost:8080/preview/digest/text")
	fmt.Println("\nPress Ctrl+C to stop the server")

	log.Fatal(http.ListenAndServe(":"+port, nil))
}
