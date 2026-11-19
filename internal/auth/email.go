package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EmailService sends verification codes via Resend API with rate limiting.
type EmailService struct {
	apiKey      string
	fromAddress string
	rateLimiter *sync.Map // email -> lastSentTime
	cooldownSec int
}

// NewEmailService creates an EmailService with rate limiting.
func NewEmailService(apiKey, fromAddress string, cooldownSec int) *EmailService {
	return &EmailService{
		apiKey:      apiKey,
		fromAddress: fromAddress,
		rateLimiter: &sync.Map{},
		cooldownSec: cooldownSec,
	}
}

// CanSendEmail checks if enough time has passed since the last email to this address.
func (e *EmailService) CanSendEmail(email string) bool {
	if e.cooldownSec <= 0 {
		return true
	}

	val, exists := e.rateLimiter.Load(email)
	if !exists {
		return true
	}

	lastSent := val.(time.Time)
	return time.Since(lastSent) >= time.Duration(e.cooldownSec)*time.Second
}

// SendVerificationCode sends a 6-digit verification code via Resend API.
func (e *EmailService) SendVerificationCode(toEmail, code string) error {
	if !e.CanSendEmail(toEmail) {
		return fmt.Errorf("rate limit: please wait %d seconds before requesting another code", e.cooldownSec)
	}

	html := generateEmailTemplate(code)

	payload := map[string]string{
		"from":    e.fromAddress,
		"to":      toEmail,
		"subject": "Claude Gateway Verification Code",
		"html":    html,
	}
	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(jsonPayload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Resend API returned HTTP %d", resp.StatusCode)
	}

	// Update rate limiter
	e.rateLimiter.Store(toEmail, time.Now())

	return nil
}

func generateEmailTemplate(code string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:sans-serif;padding:24px;">
  <h2>Claude Gateway Verification Code</h2>
  <p>Your verification code is:</p>
  <p style="font-size:32px;font-weight:bold;letter-spacing:8px;color:#4f46e5;">%s</p>
  <p style="color:#6b7280;font-size:14px;">This code will expire in 15 minutes. Do not share it with anyone.</p>
</body>
</html>`, code)
}
