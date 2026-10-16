package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/sapelyuk/smart-library/services/ai-service/gen/go/ai/v1"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/handler"
)

// stubClient records the request the gateway built and answers with canned values.
type stubClient struct {
	aiv1.AiServiceClient

	// Inputs captured by each call.
	recvRec *aiv1.RecommendRequest
	recvIng *aiv1.IngestBookRequest
	recvDel *aiv1.DeleteBookRequest

	// err, when set, is returned by the read methods to exercise the
	// gRPC-to-HTTP status mapping.
	err error
}

func (s *stubClient) Recommend(_ context.Context, in *aiv1.RecommendRequest, _ ...grpc.CallOption) (*aiv1.RecommendResponse, error) {
	s.recvRec = in
	if s.err != nil {
		return nil, s.err
	}
	return &aiv1.RecommendResponse{
		Recommendation: &aiv1.Recommendation{
			Answer: "I recommend these books.",
			Books: []*aiv1.RecommendedBook{
				{BookId: "b1", Title: "DDD Book", Author: "Evans"},
			},
			Source: aiv1.RecommendationSource_RECOMMENDATION_SOURCE_GENERATED,
			SessionId: "s1",
		},
	}, nil
}

func (s *stubClient) IngestBook(_ context.Context, in *aiv1.IngestBookRequest, _ ...grpc.CallOption) (*aiv1.IngestBookResponse, error) {
	s.recvIng = in
	if s.err != nil {
		return nil, s.err
	}
	return &aiv1.IngestBookResponse{BookId: in.GetBookId(), Title: "Clean Architecture"}, nil
}

func (s *stubClient) DeleteBook(_ context.Context, in *aiv1.DeleteBookRequest, _ ...grpc.CallOption) (*aiv1.DeleteBookResponse, error) {
	s.recvDel = in
	if s.err != nil {
		return nil, s.err
	}
	return &aiv1.DeleteBookResponse{BookId: in.GetBookId(), Deleted: true}, nil
}

func (s *stubClient) IndexStatus(_ context.Context, _ *aiv1.IndexStatusRequest, _ ...grpc.CallOption) (*aiv1.IndexStatusResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &aiv1.IndexStatusResponse{IndexedBooks: 42, IndexedChunks: 128}, nil
}

func newRESTServer(t *testing.T, client aiv1.AiServiceClient) *httptest.Server {
	t.Helper()
	h, err := handler.NewREST(client)
	if err != nil {
		t.Fatalf("NewREST: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func doReq(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeResp(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

// --- Routes ---

func TestRecommendRoute(t *testing.T) {
	client := &stubClient{}
	srv := newRESTServer(t, client)

	resp := doReq(t, http.MethodPost, srv.URL+"/v1/ai/recommendations", `{"query":"books about DDD","limit":3}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if client.recvRec.GetQuery() != "books about DDD" {
		t.Errorf("query = %q, want books about DDD", client.recvRec.GetQuery())
	}
	if client.recvRec.GetLimit() != 3 {
		t.Errorf("limit = %d, want 3", client.recvRec.GetLimit())
	}
	dec := decodeResp(t, resp)
	rec, ok := dec["recommendation"].(map[string]any)
	if !ok {
		t.Fatalf("no recommendation in response: %v", dec)
	}
	if rec["answer"] != "I recommend these books." {
		t.Errorf("answer = %v, want I recommend these books.", rec["answer"])
	}
}

func TestIngestBookRoute(t *testing.T) {
	client := &stubClient{}
	srv := newRESTServer(t, client)

	resp := doReq(t, http.MethodPost, srv.URL+"/v1/ai/index/books/"+testBookID, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if client.recvIng.GetBookId() != testBookID {
		t.Errorf("book_id = %q, want %q", client.recvIng.GetBookId(), testBookID)
	}
	dec := decodeResp(t, resp)
	if dec["bookId"] != testBookID {
		t.Errorf("bookId = %v, want %s", dec["bookId"], testBookID)
	}
}

func TestDeleteBookRoute(t *testing.T) {
	client := &stubClient{}
	srv := newRESTServer(t, client)

	resp := doReq(t, http.MethodDelete, srv.URL+"/v1/ai/index/books/"+testBookID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if client.recvDel.GetBookId() != testBookID {
		t.Errorf("book_id = %q, want %q", client.recvDel.GetBookId(), testBookID)
	}
}

func TestIndexStatusRoute(t *testing.T) {
	client := &stubClient{}
	srv := newRESTServer(t, client)

	resp := doReq(t, http.MethodGet, srv.URL+"/v1/ai/index/status", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	dec := decodeResp(t, resp)
	if dec["indexedBooks"] != float64(42) {
		t.Errorf("indexed_books = %v, want 42", dec["indexedBooks"])
	}
	if dec["indexedChunks"] != float64(128) {
		t.Errorf("indexed_chunks = %v, want 128", dec["indexedChunks"])
	}
}
// --- Status mapping ---

func TestHTTPStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"not found", status.Error(codes.NotFound, "book not found"), http.StatusNotFound},
		{"invalid argument", status.Error(codes.InvalidArgument, "bad book id"), http.StatusBadRequest},
		{"permission denied", status.Error(codes.PermissionDenied, "no access"), http.StatusForbidden},
		{"unauthenticated", status.Error(codes.Unauthenticated, "auth required"), http.StatusUnauthorized},
		{"unavailable", status.Error(codes.Unavailable, "backend down"), http.StatusServiceUnavailable},
		{"internal error", status.Error(codes.Internal, "something wrong"), http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubClient{err: tc.err}
			srv := newRESTServer(t, client)

			// Use the IndexStatus route for a simple GET that will trigger the error.
			resp := doReq(t, http.MethodGet, srv.URL+"/v1/ai/index/status", "")
			if resp.StatusCode != tc.wantCode {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}
		})
	}
}

// --- Swagger ---

func TestSwaggerEndpoints(t *testing.T) {
	srv := newRESTServer(t, &stubClient{})

	// swagger.json
	specResp, err := http.Get(srv.URL + "/swagger/swagger.json")
	if err != nil {
		t.Fatalf("get swagger.json: %v", err)
	}
	defer specResp.Body.Close()
	if specResp.StatusCode != http.StatusOK {
		t.Fatalf("swagger.json status = %d, want %d", specResp.StatusCode, http.StatusOK)
	}

	var spec map[string]any
	if err := json.NewDecoder(specResp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode swagger.json: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("swagger.json has no paths")
	}
	for _, path := range []string{"/v1/ai/recommendations", "/v1/ai/index/books/{bookId}", "/v1/ai/index/status"} {
		if _, ok := paths[path]; !ok {
			t.Errorf("swagger.json missing path %s", path)
		}
	}

	// Swagger UI
	uiResp, err := http.Get(srv.URL + "/swagger/")
	if err != nil {
		t.Fatalf("get /swagger/: %v", err)
	}
	defer uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		t.Fatalf("/swagger/ status = %d, want %d", uiResp.StatusCode, http.StatusOK)
	}

	// Root redirects to /swagger/
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	rootResp, err := noRedirect.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer rootResp.Body.Close()
	if rootResp.StatusCode != http.StatusFound {
		t.Errorf("/ = %d, want 302", rootResp.StatusCode)
	}
	if rootResp.Header.Get("Location") != "/swagger/" {
		t.Errorf("Location = %q, want /swagger/", rootResp.Header.Get("Location"))
	}
}