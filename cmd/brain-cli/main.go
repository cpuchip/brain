// brain-cli is a command-line client for the second brain relay.
// It connects to ibeco.me as the "app" role, letting you capture thoughts,
// view history, and check status from a terminal.
//
// Usage:
//
//	brain-cli capture "I should call Mom this weekend"
//	brain-cli status
//	brain-cli recent
//	brain-cli recent 10
//	brain-cli fix <thought-id> <category>
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// Message types (must match relay protocol).
const (
	typeAuth      = "auth"
	typeAuthOK    = "auth_ok"
	typeAuthError = "auth_error"
	typeThought   = "thought"
	typeResult    = "result"
	typeFix       = "fix"
	typeFixOK     = "fix_ok"
	typePresence  = "presence"
	typeQueued    = "queued"
)

func main() {
	log.SetFlags(0)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Load .env from current dir (or brain dir)
	_ = godotenv.Load()

	token := envFirst("IBECOME_TOKEN", "RELAY_TOKEN")
	baseURL := envFirst("IBECOME_URL", "RELAY_URL")

	if token == "" {
		log.Fatal("IBECOME_TOKEN (or RELAY_TOKEN) is required. Set it in .env or environment.")
	}
	if baseURL == "" {
		baseURL = "https://ibeco.me"
	}

	cmd := os.Args[1]

	switch cmd {
	case "capture", "c":
		if len(os.Args) < 3 {
			log.Fatal("Usage: brain-cli capture \"your thought here\"")
		}
		text := strings.Join(os.Args[2:], " ")
		doCapture(baseURL, token, text)

	case "status", "s":
		doStatus(baseURL, token)

	case "recent", "r":
		limit := 20
		if len(os.Args) >= 3 {
			if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 {
				limit = n
			}
		}
		doRecent(baseURL, token, limit)

	case "fix", "f":
		if len(os.Args) < 4 {
			log.Fatal("Usage: brain-cli fix <thought-id> <category>")
		}
		doFix(baseURL, token, os.Args[2], os.Args[3])

	case "help", "-h", "--help":
		printUsage()

	default:
		// If it doesn't match a command, treat the whole thing as a capture
		text := strings.Join(os.Args[1:], " ")
		doCapture(baseURL, token, text)
	}
}

func printUsage() {
	fmt.Println(`brain-cli — command-line client for the second brain

Usage:
  brain-cli capture "thought text"    Send a thought for classification
  brain-cli status                    Check agent status and queue counts
  brain-cli recent [limit]            View recent message history (default: 20)
  brain-cli fix <id> <category>       Reclassify a thought

Shortcuts:
  brain-cli c "thought"               Same as capture
  brain-cli s                         Same as status
  brain-cli r [limit]                 Same as recent
  brain-cli f <id> <cat>              Same as fix
  brain-cli "thought text"            Bare text is captured

Environment:
  IBECOME_TOKEN / RELAY_TOKEN         API token (bec_...)
  IBECOME_URL / RELAY_URL             Server URL (default: https://ibeco.me)`)
}

// envFirst returns the first non-empty value from the given env var names.
func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// wsURL converts an HTTP URL to a WebSocket URL and appends the brain path.
func wsURL(baseURL string) string {
	u := strings.TrimRight(baseURL, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/ws/brain"
}

// restURL builds a REST endpoint URL.
func restURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

// newID generates a random hex ID for thought tracking.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// doCapture sends a thought through the relay and waits for a classification result.
func doCapture(baseURL, token, text string) {
	ws := wsConnect(baseURL, token)
	defer ws.Close()

	thoughtID := newID()
	thought := map[string]string{
		"type":      typeThought,
		"id":        thoughtID,
		"text":      text,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"source":    "cli",
	}

	data, _ := json.Marshal(thought)
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Fatalf("Send failed: %v", err)
	}

	fmt.Printf("Sent: %s\n", text)
	fmt.Println("Waiting for classification...")

	// Wait for result with timeout
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
				fmt.Println("\n⏱  Timed out waiting for result.")
				fmt.Println("   The thought was queued — the agent may be offline.")
				return
			}
			log.Fatalf("Read failed: %v", err)
		}

		var env struct {
			Type string `json:"type"`
		}
		json.Unmarshal(data, &env)

		switch env.Type {
		case typeResult:
			var result struct {
				ThoughtID   string   `json:"thought_id"`
				Category    string   `json:"category"`
				Title       string   `json:"title"`
				Confidence  float64  `json:"confidence"`
				Tags        []string `json:"tags"`
				NeedsReview bool     `json:"needs_review"`
				FilePath    string   `json:"file_path"`
			}
			json.Unmarshal(data, &result)

			if result.ThoughtID != thoughtID {
				continue // Not our result
			}

			fmt.Println()
			fmt.Printf("  Category:   %s\n", result.Category)
			fmt.Printf("  Title:      %s\n", result.Title)
			fmt.Printf("  Confidence: %.0f%%\n", result.Confidence*100)
			if len(result.Tags) > 0 {
				fmt.Printf("  Tags:       %s\n", strings.Join(result.Tags, ", "))
			}
			if result.NeedsReview {
				fmt.Printf("  ⚠ Needs review (low confidence)\n")
			}
			fmt.Printf("  Path:       %s\n", result.FilePath)
			fmt.Printf("  ID:         %s\n", thoughtID)
			return

		case typePresence:
			var p struct {
				AgentOnline bool `json:"agent_online"`
			}
			json.Unmarshal(data, &p)
			if p.AgentOnline {
				fmt.Println("Agent: online ✓")
			} else {
				fmt.Println("Agent: offline — thought will be queued")
			}

		case typeQueued:
			// We might receive queued results from previous sessions
			var q struct {
				Messages []json.RawMessage `json:"messages"`
			}
			json.Unmarshal(data, &q)
			if len(q.Messages) > 0 {
				fmt.Printf("(%d queued messages delivered)\n", len(q.Messages))
				// Check if any are our result
				for _, raw := range q.Messages {
					var innerEnv struct {
						Type      string `json:"type"`
						ThoughtID string `json:"thought_id"`
					}
					json.Unmarshal(raw, &innerEnv)
					if innerEnv.Type == typeResult && innerEnv.ThoughtID == thoughtID {
						// This is our result inside queued delivery
						var result struct {
							Category    string   `json:"category"`
							Title       string   `json:"title"`
							Confidence  float64  `json:"confidence"`
							Tags        []string `json:"tags"`
							NeedsReview bool     `json:"needs_review"`
							FilePath    string   `json:"file_path"`
						}
						json.Unmarshal(raw, &result)
						fmt.Println()
						fmt.Printf("  Category:   %s\n", result.Category)
						fmt.Printf("  Title:      %s\n", result.Title)
						fmt.Printf("  Confidence: %.0f%%\n", result.Confidence*100)
						if len(result.Tags) > 0 {
							fmt.Printf("  Tags:       %s\n", strings.Join(result.Tags, ", "))
						}
						if result.NeedsReview {
							fmt.Printf("  ⚠ Needs review (low confidence)\n")
						}
						fmt.Printf("  Path:       %s\n", result.FilePath)
						fmt.Printf("  ID:         %s\n", thoughtID)
						return
					}
				}
			}

		default:
			// Ignore other messages (status, etc.)
		}
	}
}

// doFix sends a reclassification request through the relay.
func doFix(baseURL, token, thoughtID, newCategory string) {
	ws := wsConnect(baseURL, token)
	defer ws.Close()

	fix := map[string]string{
		"type":         typeFix,
		"thought_id":   thoughtID,
		"new_category": newCategory,
	}

	data, _ := json.Marshal(fix)
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Fatalf("Send failed: %v", err)
	}

	fmt.Printf("Fix requested: %s → %s\n", thoughtID, newCategory)
	fmt.Println("Waiting for confirmation...")

	ws.SetReadDeadline(time.Now().Add(30 * time.Second))

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
				fmt.Println("\n⏱  Timed out. Agent may be offline.")
				return
			}
			log.Fatalf("Read failed: %v", err)
		}

		var env struct {
			Type string `json:"type"`
		}
		json.Unmarshal(data, &env)

		if env.Type == typeFixOK {
			var ok struct {
				ThoughtID string `json:"thought_id"`
				NewPath   string `json:"new_path"`
			}
			json.Unmarshal(data, &ok)
			if ok.ThoughtID == thoughtID {
				fmt.Printf("\n  Moved to: %s\n", ok.NewPath)
				return
			}
		}
	}
}

// doStatus fetches the brain relay status via REST.
func doStatus(baseURL, token string) {
	url := restURL(baseURL, "/api/brain/status")
	data := restGet(url, token)

	var status struct {
		AgentOnline    bool `json:"agent_online"`
		PendingToAgent int  `json:"pending_to_agent"`
		PendingToApp   int  `json:"pending_to_app"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		log.Fatalf("Parse error: %v", err)
	}

	if status.AgentOnline {
		fmt.Println("Agent:           online ✓")
	} else {
		fmt.Println("Agent:           offline")
	}
	fmt.Printf("Queued → agent:  %d\n", status.PendingToAgent)
	fmt.Printf("Queued → app:    %d\n", status.PendingToApp)
}

// doRecent fetches message history via REST.
func doRecent(baseURL, token string, limit int) {
	url := restURL(baseURL, fmt.Sprintf("/api/brain/history?limit=%d", limit))
	data := restGet(url, token)

	var entries []struct {
		MessageID   string     `json:"message_id"`
		Direction   string     `json:"direction"`
		Payload     string     `json:"payload"`
		Status      string     `json:"status"`
		CreatedAt   time.Time  `json:"created_at"`
		DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Fatalf("Parse error: %v", err)
	}

	if len(entries) == 0 {
		fmt.Println("No messages yet.")
		return
	}

	for _, e := range entries {
		ts := e.CreatedAt.Local().Format("Jan 02 15:04")
		dir := "→"
		if e.Direction == "to_app" {
			dir = "←"
		}

		// Try to extract type and summary from payload
		var payload struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Category string `json:"category"`
			Title    string `json:"title"`
		}
		json.Unmarshal([]byte(e.Payload), &payload)

		var summary string
		switch payload.Type {
		case typeThought:
			summary = truncate(payload.Text, 60)
		case typeResult:
			summary = fmt.Sprintf("%s/%s", payload.Category, payload.Title)
		case typeFix:
			summary = fmt.Sprintf("fix → %s", payload.Category)
		case typeFixOK:
			summary = "fix confirmed"
		default:
			summary = payload.Type
		}

		delivered := " "
		if e.Status == "delivered" {
			delivered = "✓"
		}

		fmt.Printf("[%s] %s %s %s %s\n", ts, dir, delivered, payload.Type, summary)
	}
}

// truncate shortens a string to maxLen, adding "..." if needed.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// wsConnect establishes a WebSocket connection and authenticates.
func wsConnect(baseURL, token string) *websocket.Conn {
	url := wsURL(baseURL)

	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Fatalf("Connection failed: %v\nURL: %s", err, url)
	}

	// Authenticate as app
	authMsg, _ := json.Marshal(map[string]string{
		"type":  typeAuth,
		"token": token,
		"role":  "app",
	})
	if err := ws.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		ws.Close()
		log.Fatalf("Auth send failed: %v", err)
	}

	// Read auth response
	_, data, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		log.Fatalf("Auth read failed: %v", err)
	}

	var env struct {
		Type  string `json:"type"`
		Error string `json:"error,omitempty"`
	}
	json.Unmarshal(data, &env)

	switch env.Type {
	case typeAuthOK:
		// good
	case typeAuthError:
		ws.Close()
		log.Fatalf("Authentication failed: %s", env.Error)
	default:
		ws.Close()
		log.Fatalf("Unexpected auth response: %s", env.Type)
	}

	return ws
}

// restGet performs an authenticated GET request and returns the response body.
func restGet(url, token string) []byte {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Fatalf("Request error: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Server error (%d): %s", resp.StatusCode, string(body))
	}

	return body
}
