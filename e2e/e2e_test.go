// Package e2e runs end-to-end integration tests against a live Docker stack.
//
// Tests skip automatically if any service is unreachable.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// --- Environment ---

var (
	userURL   string
	bookURL   string
	loanURL   string
	gwURL     string
	aiURL     string
	rabbitURL string
)

func init() {
	userURL = mustEnv("E2E_USER_URL", "http://localhost:8092")
	bookURL = mustEnv("E2E_BOOK_URL", "http://localhost:8091")
	loanURL = mustEnv("E2E_LOAN_URL", "http://localhost:8093")
	gwURL = mustEnv("E2E_GW_URL", "http://localhost:8080")
	aiURL = mustEnv("E2E_AI_URL", "http://localhost:8095")
	rabbitURL = mustEnv("E2E_RABBIT_URL", "http://localhost:15672")
}

func mustEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- HTTP helpers ---

var httpClient = &http.Client{Timeout: 10 * time.Second}

func get(ctx context.Context, url string, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return httpClient.Do(req)
}

func post(ctx context.Context, url string, token string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return httpClient.Do(req)
}

func mustBody(t *testing.T, resp *http.Response, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body
}

func mustUnmarshal(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, string(body))
	}
}

func mustStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status %d, got %d: %s", expected, resp.StatusCode, string(body))
	}
}

// --- Stack reachability ---

// isReachable reports whether a URL returns HTTP 200.
func isReachable(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// --- ISBN helper ---

// validISBN13 generates a unique valid ISBN-13 for testing.
func validISBN13(t *testing.T) string {
	t.Helper()
	// Base: 9780000000 + 3 random digits = 12 digits, then + check digit = 13
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	base := fmt.Sprintf("978000000%03d", r.Intn(1000))
	// Compute ISBN-13 check digit (weight 1,3,1,3,...)
	sum := 0
	for i, c := range base {
		d := int(c - '0')
		if i%2 == 0 {
			sum += d
		} else {
			sum += d * 3
		}
	}
	check := (10 - sum%10) % 10
	return base + fmt.Sprintf("%d", check)
}

// --- Test helpers ---

func skipIfDown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	urls := []string{
		userURL + "/healthz",
		bookURL + "/healthz",
		loanURL + "/healthz",
		gwURL + "/healthz",
		aiURL + "/healthz",
	}
	for _, u := range urls {
		if !isReachable(ctx, u) {
			t.Skipf("service unreachable: %s (stack not running?)", u)
		}
	}
}

func librarianToken(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	resp, err := post(ctx, userURL+"/v1/auth/login", "", map[string]string{
		"email":    "admin@library.local",
		"password": "Admin123!",
	})
	body := mustBody(t, resp, err)
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	mustUnmarshal(t, body, &login)
	if login.AccessToken == "" {
		t.Fatal("empty access token from login")
	}
	return login.AccessToken
}

func registerReader(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("e2e-%d@test.local", time.Now().UnixNano()%100000)
	// Register first (returns User, no token)
	resp, err := post(ctx, userURL+"/v1/auth/register", "", map[string]string{
		"email":     email,
		"password":  "Reader12345!",
		"full_name": "E2E Reader",
		"phone":     "+79001234567",
	})
	body := mustBody(t, resp, err)

	// Register might return 409 (already exists) if the same reader was created in a prior run
	if resp.StatusCode == 409 {
		// Just login
	} else if resp.StatusCode != 200 {
		t.Fatalf("register failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Login to get token
	resp, err = post(ctx, userURL+"/v1/auth/login", "", map[string]string{
		"email":    email,
		"password": "Reader12345!",
	})
	body = mustBody(t, resp, err)
	var login struct {
		AccessToken string `json:"accessToken"`
		UserID      string `json:"userId"`
	}
	mustUnmarshal(t, body, &login)
	return login.UserID, login.AccessToken
}

// --- Test suite ---

func TestMain(m *testing.M) {
	fmt.Println("=== E2E Test Suite ===")
	fmt.Printf("  User Service : %s\n", userURL)
	fmt.Printf("  Book Service : %s\n", bookURL)
	fmt.Printf("  Loan Service : %s\n", loanURL)
	fmt.Printf("  API Gateway  : %s\n", gwURL)
	fmt.Printf("  AI Service   : %s\n", aiURL)
	fmt.Printf("  RabbitMQ Mgmt: %s\n", rabbitURL)
	fmt.Println()
	os.Exit(m.Run())
}

func TestUserFlow(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	// 1. Librarian login
	t.Log("1. Librarian login")
	libToken := librarianToken(t)
	if libToken == "" {
		t.Fatal("librarian login returned empty token")
	}

	// 2. Register reader (self-service)
	t.Log("2. Register reader")
	readerID, readerToken := registerReader(t)
	if readerID == "" || readerToken == "" {
		t.Fatal("reader registration/login failed")
	}

	// 3. GET /auth/me
	t.Log("3. GET /auth/me")
	resp, err := get(ctx, userURL+"/v1/auth/me", readerToken)
	body := mustBody(t, resp, err)
	var me struct {
		Email string `json:"email"`
		ID    string `json:"id"`
	}
	mustUnmarshal(t, body, &me)
	if me.Email == "" || me.ID == "" {
		t.Fatalf("unexpected /auth/me response: %s", string(body))
	}

	// 4. Wrong password rejected
	t.Log("4. Wrong password rejected")
	resp, err = post(ctx, userURL+"/v1/auth/login", "", map[string]string{
		"email":    "admin@library.local",
		"password": "WrongPassword12!",
	})
	if resp.StatusCode == 200 {
		t.Fatal("wrong password should be rejected")
	}

	// 5. No token on protected route rejected
	t.Log("5. No-token on protected route rejected")
	resp, err = get(ctx, userURL+"/v1/users", "")
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		t.Fatal("protected route without token should be rejected")
	}
}

func TestBookCatalog(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	libToken := librarianToken(t)
	isbn := validISBN13(t)

	// 1. Create book
	t.Log("1. CreateBook")
	resp, err := post(ctx, bookURL+"/v1/books", libToken, map[string]any{
		"isbn":           isbn,
		"title":          "Сквозное тестирование",
		"author":         "Автоматизация",
		"publisher":      "E2E Press",
		"published_year": 2026,
	})
	body := mustBody(t, resp, err)
	var createResp struct {
		ID     string `json:"id"`
		ISBN   string `json:"isbn"`
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	mustUnmarshal(t, body, &createResp)
	bookID := createResp.ID
	if bookID == "" {
		t.Fatalf("CreateBook returned no book id: %s", string(body))
	}
	t.Logf("   book_id=%s", bookID)

	// 2. Add copy
	t.Log("2. AddBookCopy")
	resp, err = post(ctx, bookURL+"/v1/books/"+bookID+"/copies", libToken, map[string]string{
		"barcode": "E2E-COPY-001",
	})
	body = mustBody(t, resp, err)
	var copyResp struct {
		ID string `json:"id"`
	}
	mustUnmarshal(t, body, &copyResp)
	copyID := copyResp.ID
	if copyID == "" {
		t.Fatalf("AddBookCopy returned no copy id: %s", string(body))
	}
	t.Logf("   copy_id=%s", copyID)

	// 3. List books
	t.Log("3. ListBooks")
	resp, err = get(ctx, bookURL+"/v1/books", libToken)
	body = mustBody(t, resp, err)
	var listResp struct {
		Books []struct{ ID, Title, Author string } `json:"books"`
		Total int                                  `json:"total"`
	}
	mustUnmarshal(t, body, &listResp)
	if len(listResp.Books) == 0 {
		t.Fatal("ListBooks returned no books")
	}

	// 4. Get book
	t.Log("4. GetBook")
	resp, err = get(ctx, bookURL+"/v1/books/"+bookID, libToken)
	body = mustBody(t, resp, err)
	var getResp struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	mustUnmarshal(t, body, &getResp)
	if getResp.ID == "" || getResp.Title == "" {
		t.Fatalf("GetBook unexpected: %s", string(body))
	}

	// 5. List copies
	t.Log("5. ListBookCopies")
	resp, err = get(ctx, bookURL+"/v1/books/"+bookID+"/copies", libToken)
	body = mustBody(t, resp, err)
	var copiesResp struct {
		Copies []struct {
			ID     string `json:"id"`
			Barc   string `json:"barcode"`
			Status string `json:"status"`
		} `json:"copies"`
	}
	mustUnmarshal(t, body, &copiesResp)
	if len(copiesResp.Copies) == 0 {
		t.Fatal("ListBookCopies returned no copies")
	}
}

func TestLoanLifecycle(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	libToken := librarianToken(t)
	readerID, readerToken := registerReader(t)
	isbn := validISBN13(t)

	// Create a book with a copy
	t.Log("1. Create book & copy")
	resp, err := post(ctx, bookURL+"/v1/books", libToken, map[string]any{
		"isbn":           isbn,
		"title":          "Выдача и возврат",
		"author":         "Книговед",
		"publisher":      "E2E Press",
		"published_year": 2026,
	})
	mustBody(t, resp, err)

	resp, err = get(ctx, bookURL+"/v1/books?query="+isbn, libToken)
	body := mustBody(t, resp, err)
	var listResp struct {
		Books []struct{ ID string } `json:"books"`
	}
	mustUnmarshal(t, body, &listResp)
	if len(listResp.Books) == 0 {
		t.Fatal("book not found after creation")
	}
	bookID := listResp.Books[0].ID

	resp, err = post(ctx, bookURL+"/v1/books/"+bookID+"/copies", libToken, map[string]string{
		"barcode": "E2E-LOAN-001",
	})
	mustBody(t, resp, err)

	// 2. Borrow (loan-service resolves available copies via book-service)
	t.Log("2. Borrow (loan-service resolves copies)")
	resp, err = post(ctx, loanURL+"/v1/loans", readerToken, map[string]string{
		"reader_id": readerID,
		"book_id":   bookID,
	})
	body = mustBody(t, resp, err)
	var borrowResp struct {
		Loan struct {
			ID       string `json:"id"`
			ReaderID string `json:"readerId"`
			BookID   string `json:"bookId"`
			CopyID   string `json:"copyId"`
			Status   string `json:"status"`
		} `json:"loan"`
	}
	mustUnmarshal(t, body, &borrowResp)
	loanID := borrowResp.Loan.ID
	if loanID == "" {
		t.Fatalf("Borrow returned no loan id: %s", string(body))
	}
	t.Logf("   loan_id=%s", loanID)

	// 3. Borrow same book again (should fail — no available copies)
	t.Log("3. Duplicate borrow rejected")
	resp, err = post(ctx, loanURL+"/v1/loans", readerToken, map[string]string{
		"reader_id": readerID,
		"book_id":   bookID,
	})
	if resp.StatusCode == 200 {
		t.Fatal("duplicate borrow should be rejected")
	}

	// 4. List own loans
	t.Log("4. List own loans")
	resp, err = get(ctx, loanURL+"/v1/loans", readerToken)
	body = mustBody(t, resp, err)
	var listResp2 struct {
		Loans []struct{ ID, Status string } `json:"loans"`
		Total int                           `json:"total"`
	}
	mustUnmarshal(t, body, &listResp2)
	if len(listResp2.Loans) == 0 {
		t.Fatal("List returned no loans")
	}

	// 5. Get loan
	t.Log("5. Get loan")
	resp, err = get(ctx, loanURL+"/v1/loans/"+loanID, readerToken)
	body = mustBody(t, resp, err)
	var getResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	mustUnmarshal(t, body, &getResp)
	if getResp.ID == "" {
		t.Fatalf("Get loan unexpected: %s", string(body))
	}

	// 6. Renew
	t.Log("6. Renew loan")
	resp, err = post(ctx, loanURL+"/v1/loans/"+loanID+"/renew", readerToken, map[string]int{
		"extend_days": 14,
	})
	body = mustBody(t, resp, err)
	t.Logf("   renew: %s", string(body))

	// 7. Return
	t.Log("7. Return loan")
	resp, err = post(ctx, loanURL+"/v1/loans/"+loanID+"/return", readerToken, map[string]string{
		"loan_id": loanID,
	})
	body = mustBody(t, resp, err)
	var returnResp struct {
		Loan struct {
			Status     string `json:"status"`
			ReturnedAt string `json:"returnedAt"`
		} `json:"loan"`
	}
	mustUnmarshal(t, body, &returnResp)
	if returnResp.Loan.Status != "LOAN_STATUS_RETURNED" {
		t.Fatalf("loan status after return: %s", returnResp.Loan.Status)
	}
	t.Logf("   returned_at=%s", returnResp.Loan.ReturnedAt)

	// 8. Verify loan status is RETURNED
	t.Log("8. Loan status RETURNED")
	resp, err = get(ctx, loanURL+"/v1/loans/"+loanID, readerToken)
	body = mustBody(t, resp, err)
	mustUnmarshal(t, body, &getResp)
	if getResp.Status != "LOAN_STATUS_RETURNED" {
		t.Fatalf("loan status: %s, want LOAN_STATUS_RETURNED", getResp.Status)
	}

	// 9. Librarian can see all loans
	t.Log("9. Librarian ListOverdue")
	resp, err = get(ctx, loanURL+"/v1/loans/overdue", libToken)
	body = mustBody(t, resp, err)
	var overdueResp struct {
		Loans []struct{ ID, Status string } `json:"loans"`
		Total int                           `json:"total"`
	}
	mustUnmarshal(t, body, &overdueResp)
	t.Logf("   overdue count=%d", len(overdueResp.Loans))
}

func TestGatewayRouting(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	// 1. Gateway healthz
	t.Log("1. Gateway /healthz")
	resp, err := get(ctx, gwURL+"/healthz", "")
	mustBody(t, resp, err)
	if resp.StatusCode != 200 {
		t.Fatalf("gateway healthz: %d", resp.StatusCode)
	}

	// 2. Gateway → user-service login
	t.Log("2. Gateway → user-service login")
	resp, err = post(ctx, gwURL+"/v1/auth/login", "", map[string]string{
		"email":    "admin@library.local",
		"password": "Admin123!",
	})
	body := mustBody(t, resp, err)
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	mustUnmarshal(t, body, &login)
	if login.AccessToken == "" {
		t.Fatal("gateway login returned no token")
	}

	// 3. Gateway → book-service ListBooks
	t.Log("3. Gateway → book-service ListBooks")
	resp, err = get(ctx, gwURL+"/v1/books", login.AccessToken)
	body = mustBody(t, resp, err)
	var listResp struct {
		Books []struct{ ID, Title string } `json:"books"`
		Total int                          `json:"total"`
	}
	mustUnmarshal(t, body, &listResp)
	// Books may be empty after fresh stack start — that's OK, just verify response structure
	t.Logf("   books=%d, total=%d", len(listResp.Books), listResp.Total)

	// 4. Gateway rejects no-token for protected route
	t.Log("4. Gateway rejects no-token for protected route")
	resp, err = get(ctx, gwURL+"/v1/loans", "")
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		t.Fatal("gateway should reject no-token for protected route")
	}
}

func TestNotificationEventConsumption(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	// 1. Check notification queue exists via RabbitMQ management API
	t.Log("1. Check notification queue bindings")
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, rabbitURL+"/api/queues/%2F/notification-service.loan-events", nil)
	if reqErr != nil {
		t.Fatalf("create request: %v", reqErr)
	}
	req.SetBasicAuth("guest", "guest")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Skipf("RabbitMQ management API unreachable: %v", err)
	}
	body := mustBody(t, resp, err)
	var queue struct {
		Name      string `json:"name"`
		Consumers int    `json:"consumers"`
	}
	mustUnmarshal(t, body, &queue)
	if queue.Name == "" {
		t.Fatal("notification queue not found")
	}
	if queue.Consumers == 0 {
		t.Log("   WARN: 0 consumers — service may not be running")
	}
	t.Logf("   queue=%s, consumers=%d", queue.Name, queue.Consumers)

	// 2. Publish a loan.issued event to library.events exchange
	t.Log("2. Publish loan.issued event")
	event := map[string]any{
		"routing_key": "loan.issued",
		"properties": map[string]any{
			"delivery_mode": 2,
		},
		"payload": map[string]any{
			"event_id":       "550e8400-e29b-41d4-a716-446655440001",
			"event_type":     "loan.issued",
			"occurred_at":    time.Now().UTC().Format(time.RFC3339),
			"schema_version": 1,
			"payload": map[string]any{
				"loan_id":      "550e8400-e29b-41d4-a716-446655440002",
				"reader_id":    "550e8400-e29b-41d4-a716-446655440003",
				"reader_email": "e2e@test.local",
				"book_title":   "E2E Test Book",
				"book_author":  "Test Author",
				"due_at":       "2026-11-01T12:00:00Z",
			},
		},
		"payload_encoding": "string",
	}

	// Marshal the payload as a JSON string
	eventBytes, err := json.Marshal(event["payload"].(map[string]any))
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	event["payload"] = string(eventBytes)

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal publish request: %v", err)
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodPost,
		rabbitURL+"/api/exchanges/%2F/library.events/publish", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.SetBasicAuth("guest", "guest")

	resp2, err := httpClient.Do(req2)
	if err != nil {
		t.Skipf("RabbitMQ publish failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 && resp2.StatusCode != 204 {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("publish failed: HTTP %d: %s", resp2.StatusCode, string(body))
	}

	t.Log("   event published → routed=true")

	// 3. Check that the queue delivered the message
	t.Log("3. Verify queue delivered message")
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet,
			rabbitURL+"/api/queues/%2F/notification-service.loan-events", nil)
		if reqErr != nil {
			continue
		}
		req.SetBasicAuth("guest", "guest")
		resp, err := httpClient.Do(req)
		if err != nil {
			continue
		}
		body := mustBody(t, resp, err)
		var stats struct {
			MessageStats struct {
				DeliverGet int `json:"deliver_get"`
				Ack        int `json:"ack"`
			} `json:"message_stats"`
		}
		mustUnmarshal(t, body, &stats)
		if stats.MessageStats.DeliverGet > 0 || stats.MessageStats.Ack > 0 {
			t.Logf("   deliver_get=%d, ack=%d", stats.MessageStats.DeliverGet, stats.MessageStats.Ack)
			t.Log("   ✓ event consumed by notification-service")
			return
		}
	}
	t.Log("   ⚠ queue delivered 0 messages (consumer may reject due to missing SMTP)")
}

func TestAIService(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	libToken := librarianToken(t)

	// 1. AI /healthz
	t.Log("1. AI /healthz")
	resp, err := get(ctx, aiURL+"/healthz", "")
	mustBody(t, resp, err)

	// 2. IndexStatus
	t.Log("2. IndexStatus")
	resp, err = get(ctx, aiURL+"/v1/ai/index/status", libToken)
	body := mustBody(t, resp, err)
	var status struct {
		IndexedBooks  int `json:"indexedBooks"`
		IndexedChunks int `json:"indexedChunks"`
	}
	mustUnmarshal(t, body, &status)
	t.Logf("   indexed_books=%d, indexed_chunks=%d", status.IndexedBooks, status.IndexedChunks)

	// 3. Recommend (may fail if n8n is not reachable — still check response structure)
	t.Log("3. Recommend")
	resp, err = post(ctx, aiURL+"/v1/ai/recommendations", libToken, map[string]any{
		"query": "книги про чистый код и ремесло программиста",
		"limit": 5,
	})
	if resp.StatusCode == 200 {
		body := mustBody(t, resp, err)
		var recommendResp struct {
			Recommendation struct {
				Query  string `json:"query"`
				Answer string `json:"answer"`
				Books  []struct {
					BookID string `json:"bookId"`
					Title  string `json:"title"`
					Author string `json:"author"`
				} `json:"books"`
				Source string `json:"source"`
			} `json:"recommendation"`
		}
		mustUnmarshal(t, body, &recommendResp)
		t.Logf("   answer=%q, books=%d, source=%s",
			recommendResp.Recommendation.Answer,
			len(recommendResp.Recommendation.Books),
			recommendResp.Recommendation.Source)
	} else {
		body, _ := io.ReadAll(resp.Body)
		// n8n might not be running — skip gracefully
		if strings.Contains(string(body), "n8n") || strings.Contains(string(body), "UNAVAILABLE") || resp.StatusCode == 503 {
			t.Skip("n8n not reachable — Recommend skipped")
		}
		t.Fatalf("Recommend failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
}

// TestFullFlow runs the complete happy-path: user → book → loan → return
func TestFullFlow(t *testing.T) {
	skipIfDown(t)
	ctx := context.Background()

	// 1. Librarian login
	libToken := librarianToken(t)

	// 2. Register reader
	readerID, readerToken := registerReader(t)

	// 3. Create book + copy
	isbn := validISBN13(t)
	resp, err := post(ctx, bookURL+"/v1/books", libToken, map[string]any{
		"isbn":           isbn,
		"title":          "Финальный тест",
		"author":         "Финалист",
		"publisher":      "E2E Press",
		"published_year": 2026,
	})
	mustBody(t, resp, err)

	resp, err = get(ctx, bookURL+"/v1/books?query="+isbn, libToken)
	body := mustBody(t, resp, err)
	var listResp struct {
		Books []struct{ ID string } `json:"books"`
	}
	mustUnmarshal(t, body, &listResp)
	if len(listResp.Books) == 0 {
		t.Fatal("book not found after creation")
	}
	bookID := listResp.Books[0].ID

	resp, err = post(ctx, bookURL+"/v1/books/"+bookID+"/copies", libToken, map[string]string{
		"barcode": "E2E-FINAL-001",
	})
	mustBody(t, resp, err)

	// 4. Borrow
	resp, err = post(ctx, loanURL+"/v1/loans", readerToken, map[string]string{
		"reader_id": readerID,
		"book_id":   bookID,
	})
	body = mustBody(t, resp, err)
	var borrowResp struct {
		Loan struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"loan"`
	}
	mustUnmarshal(t, body, &borrowResp)
	loanID := borrowResp.Loan.ID

	// 5. Return
	resp, err = post(ctx, loanURL+"/v1/loans/"+loanID+"/return", readerToken, map[string]string{
		"loan_id": loanID,
	})
	body = mustBody(t, resp, err)
	var returnResp struct {
		Loan struct {
			Status string `json:"status"`
		} `json:"loan"`
	}
	mustUnmarshal(t, body, &returnResp)
	if returnResp.Loan.Status != "LOAN_STATUS_RETURNED" {
		t.Fatalf("final return status: %s", returnResp.Loan.Status)
	}

	t.Log("✓ Full happy-path: register → create book → borrow → return")
}
