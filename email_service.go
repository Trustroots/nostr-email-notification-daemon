package main

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/vanng822/go-premailer/premailer"
	"gopkg.in/gomail.v2"
)

// EmailTemplateData represents the data structure for email templates
type EmailTemplateData struct {
	// User data
	Name      string
	FirstName string
	Email     string
	Username  string

	// URLs
	HeaderURL        string
	FooterURL        string
	SupportURL       string
	ProfileURL       string
	SenderProfileURL string

	// Email content
	Subject   string
	Title     string
	MailTitle string

	// Sender info
	From EmailSender

	// Campaign tracking
	UTMCampaign       string
	SparkpostCampaign string

	// Custom content
	Content map[string]interface{}

	// Nostr specific fields
	EventContent  string
	EventID       string
	CreatedAt     string
	SenderNIP5    string
	SenderNpub    string
	RecipientNpub string
}

// EmailSender represents sender information
type EmailSender struct {
	Name    string
	Address string
}

// EmailService handles email composition and sending
type EmailService struct {
	SMTPHost      string
	SMTPPort      int
	SMTPUsername  string
	SMTPPassword  string
	FromEmail     string
	FromName      string
	htmlTemplates *template.Template
	textTemplates *template.Template
}

// EmailTemplate represents an email template
type EmailTemplate struct {
	Subject     string
	HTMLContent string
	TextContent string
}

// EmailJob represents an email to be sent
type EmailJob struct {
	To      string
	Subject string
	HTML    string
	Text    string
}

// extractUsernameFromNIP5 extracts the username from a NIP-5 identifier
// e.g., "nostroots@trustroots.org" -> "nostroots"
func extractUsernameFromNIP5(nip5 string) string {
	if nip5 == "" {
		return ""
	}

	// Split by @ and take the first part
	parts := strings.Split(nip5, "@")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// getRecipientNpub gets the npub for a user (this would need to be passed from the main function)
// For now, we'll use a placeholder that gets replaced in the template
func getRecipientNpub(user User) string {
	return user.NostrNpub
}

// NewEmailService creates a new email service
func NewEmailService(smtpHost string, smtpPort int, smtpUsername, smtpPassword, fromEmail, fromName string) *EmailService {
	// Load HTML templates
	htmlTemplates, err := template.ParseGlob("templates/html/*.html")
	if err != nil {
		log.Printf("Warning: Failed to load HTML templates: %v", err)
		htmlTemplates = template.New("html")
	}

	// Load text templates
	textTemplates, err := template.ParseGlob("templates/text/*.txt")
	if err != nil {
		log.Printf("Warning: Failed to load text templates: %v", err)
		textTemplates = template.New("text")
	}

	return &EmailService{
		SMTPHost:      smtpHost,
		SMTPPort:      smtpPort,
		SMTPUsername:  smtpUsername,
		SMTPPassword:  smtpPassword,
		FromEmail:     fromEmail,
		FromName:      fromName,
		htmlTemplates: htmlTemplates,
		textTemplates: textTemplates,
	}
}

// renderHTMLTemplate renders the HTML email template
func (es *EmailService) renderHTMLTemplate(templateName string, data EmailTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := es.htmlTemplates.ExecuteTemplate(&buf, templateName+".html", data); err != nil {
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
func (es *EmailService) renderTextTemplate(templateName string, data EmailTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := es.textTemplates.ExecuteTemplate(&buf, templateName+".txt", data); err != nil {
		return "", fmt.Errorf("failed to execute text template %s: %v", templateName, err)
	}

	return buf.String(), nil
}

// SendEmail sends an email using the configured SMTP settings
func (es *EmailService) SendEmail(to, subject, htmlContent, textContent string) error {
	m := gomail.NewMessage()
	m.SetHeader("From", m.FormatAddress(es.FromEmail, es.FromName))
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", textContent)
	m.AddAlternative("text/html", htmlContent)

	d := gomail.NewDialer(es.SMTPHost, es.SMTPPort, es.SMTPUsername, es.SMTPPassword)

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to send email: %v", err)
	}

	return nil
}

// QueueEmailJob queues an email for background processing
func (es *EmailService) QueueEmailJob(job EmailJob) {
	// For now, we'll process emails synchronously
	// In a production system, you'd use a proper job queue like asynq
	go func() {
		if err := es.SendEmail(job.To, job.Subject, job.HTML, job.Text); err != nil {
			log.Printf("❌ Failed to send email to %s: %v", job.To, err)
		} else {
			log.Printf("✅ Email sent to %s", job.To)
		}
	}()
}

// ProcessNostrDirectMessage processes a Nostr direct message and sends an email
func (es *EmailService) ProcessNostrDirectMessage(event *nostr.Event, recipientUser User, senderNIP5 string) error {
	// Generate email template for direct message
	template, err := es.GenerateNostrDirectMessageEmail(event, recipientUser, senderNIP5)
	if err != nil {
		return fmt.Errorf("failed to generate DM email template: %v", err)
	}

	// Queue email job
	job := EmailJob{
		To:      recipientUser.Email,
		Subject: template.Subject,
		HTML:    template.HTMLContent,
		Text:    template.TextContent,
	}

	es.QueueEmailJob(job)
	return nil
}

// GenerateNostrDirectMessageEmail creates an email for a Nostr direct message
func (es *EmailService) GenerateNostrDirectMessageEmail(event *nostr.Event, recipientUser User, senderNIP5 string) (*EmailTemplate, error) {
	// Extract sender username from NIP-5 identifier
	senderUsername := extractUsernameFromNIP5(senderNIP5)

	// Create email data
	data := EmailTemplateData{
		Username:      recipientUser.Username,
		Name:          recipientUser.Username,
		FirstName:     recipientUser.Username,
		Email:         recipientUser.Email,
		SenderNIP5:    senderNIP5,
		EventContent:  event.Content,
		EventID:       event.ID,
		CreatedAt:     event.CreatedAt.Time().Format("2006-01-02 15:04:05 UTC"),
		SenderNpub:    event.PubKey,
		RecipientNpub: recipientUser.NostrNpub,
		Title:         "🔒 New Encrypted Direct Message",
		Subject:       fmt.Sprintf("🔒 Encrypted DM from %s", senderNIP5),
		From: EmailSender{
			Name:    "Trustroots Nostr",
			Address: es.FromEmail,
		},
		SupportURL:       "https://trustroots.org/support",
		FooterURL:        "https://trustroots.org",
		ProfileURL:       fmt.Sprintf("https://www.trustroots.org/profile/%s", recipientUser.Username),
		SenderProfileURL: fmt.Sprintf("https://www.trustroots.org/profile/%s", senderUsername),
		Content: map[string]interface{}{
			"buttonURL":  fmt.Sprintf("https://tripch.at/#dm:%s", event.PubKey),
			"buttonText": "View on TRipch.at",
		},
	}

	// Generate HTML content
	htmlContent, err := es.renderHTMLTemplate("nostr_direct_message", data)
	if err != nil {
		return nil, fmt.Errorf("failed to render HTML template: %v", err)
	}

	// Generate text content
	textContent, err := es.renderTextTemplate("nostr_direct_message", data)
	if err != nil {
		return nil, fmt.Errorf("failed to render text template: %v", err)
	}

	return &EmailTemplate{
		Subject:     data.Subject,
		HTMLContent: htmlContent,
		TextContent: textContent,
	}, nil
}
