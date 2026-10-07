package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	loanv1 "github.com/sapelyuk/smart-library/services/loan-service/gen/go/loan/v1"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/handler"
)

// stubClient records the request the gateway built and answers with canned
// values; every method is implemented so the gateway never hits a nil stub.
type stubClient struct {
	loanv1.LoanServiceClient

	borrowIn  *loanv1.BorrowRequest
	returnIn  *loanv1.ReturnRequest
	renewIn   *loanv1.RenewRequest
	getIn     *loanv1.GetRequest
	listIn    *loanv1.ListRequest
	overdueIn *loanv1.ListOverdueRequest

	// err, when set, is returned by the read methods to exercise the
	// gRPC-to-HTTP status mapping.
	err error
}

const (
	loanID   = "11111111-1111-1111-1111-111111111111"
	readerID = "22222222-2222-2222-2222-222222222222"
	bookID   = "33333333-3333-3333-3333-333333333333"
	copyID   = "44444444-4444-4444-4444-444444444444"
)

// fixedTime is a stable timestamp for the canned responses.
func fixedTime() time.Time {
	return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
}

func cannedLoan() *loanv1.Loan {
	return &loanv1.Loan{
		Id:         loanID,
		ReaderId:   readerID,
		BookId:     bookID,
		CopyId:     copyID,
		BorrowedAt: timestamppb.New(fixedTime()),
		DueAt:      timestamppb.New(fixedTime()),
		Status:     loanv1.LoanStatus_LOAN_STATUS_ACTIVE,
	}
}

func (s *stubClient) Borrow(_ context.Context, in *loanv1.BorrowRequest, _ ...grpc.CallOption) (*loanv1.BorrowResponse, error) {
	s.borrowIn = in

	if s.err != nil {
		return nil, s.err
	}

	return &loanv1.BorrowResponse{Loan: cannedLoan()}, nil
}

func (s *stubClient) Return(_ context.Context, in *loanv1.ReturnRequest, _ ...grpc.CallOption) (*loanv1.ReturnResponse, error) {
	s.returnIn = in

	if s.err != nil {
		return nil, s.err
	}

	loan := cannedLoan()
	loan.Status = loanv1.LoanStatus_LOAN_STATUS_RETURNED

	return &loanv1.ReturnResponse{Loan: loan}, nil
}

func (s *stubClient) Renew(_ context.Context, in *loanv1.RenewRequest, _ ...grpc.CallOption) (*loanv1.RenewResponse, error) {
	s.renewIn = in

	if s.err != nil {
		return nil, s.err
	}

	return &loanv1.RenewResponse{Loan: cannedLoan()}, nil
}

func (s *stubClient) Get(_ context.Context, in *loanv1.GetRequest, _ ...grpc.CallOption) (*loanv1.Loan, error) {
	s.getIn = in

	if s.err != nil {
		return nil, s.err
	}

	return cannedLoan(), nil
}

func (s *stubClient) List(_ context.Context, in *loanv1.ListRequest, _ ...grpc.CallOption) (*loanv1.ListResponse, error) {
	s.listIn = in

	if s.err != nil {
		return nil, s.err
	}

	return &loanv1.ListResponse{Loans: []*loanv1.Loan{cannedLoan()}, Total: 1}, nil
}

func (s *stubClient) ListOverdue(_ context.Context, in *loanv1.ListOverdueRequest, _ ...grpc.CallOption) (*loanv1.ListOverdueResponse, error) {
	s.overdueIn = in

	if s.err != nil {
		return nil, s.err
	}

	return &loanv1.ListOverdueResponse{Loans: []*loanv1.Loan{cannedLoan()}, Total: 1}, nil
}

func newServer(t *testing.T, client loanv1.LoanServiceClient) *httptest.Server {
	t.Helper()

	httpHandler, err := handler.NewREST(client)
	if err != nil {
		t.Fatalf("build rest handler: %v", err)
	}

	server := httptest.NewServer(httpHandler)
	t.Cleanup(server.Close)

	return server
}

// doRequest issues an HTTP call with an optional JSON body and returns the
// response; the body is closed when the test finishes.
func doRequest(t *testing.T, method, url, body string) *http.Response {
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

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	return decoded
}

func TestBorrowRoute(t *testing.T) {
	client := &stubClient{}
	server := newServer(t, client)

	body := `{"reader_id":"` + readerID + `","book_id":"` + bookID + `"}`

	resp := doRequest(t, http.MethodPost, server.URL+"/v1/loans", body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if client.borrowIn.GetReaderId() != readerID || client.borrowIn.GetBookId() != bookID {
		t.Errorf("request = %+v, want the body mapped onto the message", client.borrowIn)
	}

	decoded := decode(t, resp)

	loan, ok := decoded["loan"].(map[string]any)
	if !ok {
		t.Fatalf("response has no loan object: %v", decoded)
	}

	if got := loan["id"]; got != loanID {
		t.Errorf("loan.id = %v, want %s", got, loanID)
	}

	// The enum travels as its symbolic name so the Swagger UI stays readable.
	if got := loan["status"]; got != "LOAN_STATUS_ACTIVE" {
		t.Errorf("loan.status = %v, want LOAN_STATUS_ACTIVE", got)
	}
}

func TestReturnRoute(t *testing.T) {
	client := &stubClient{}
	server := newServer(t, client)

	resp := doRequest(t, http.MethodPost, server.URL+"/v1/loans/"+loanID+"/return", "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if client.returnIn.GetLoanId() != loanID {
		t.Errorf("loan_id = %q, want the id from the path", client.returnIn.GetLoanId())
	}

	decoded := decode(t, resp)

	loan, ok := decoded["loan"].(map[string]any)
	if !ok {
		t.Fatalf("response has no loan object: %v", decoded)
	}

	if got := loan["status"]; got != "LOAN_STATUS_RETURNED" {
		t.Errorf("loan.status = %v, want LOAN_STATUS_RETURNED", got)
	}
}

func TestRenewRoute(t *testing.T) {
	client := &stubClient{}
	server := newServer(t, client)

	resp := doRequest(t, http.MethodPost, server.URL+"/v1/loans/"+loanID+"/renew", `{"extend_days":7}`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if client.renewIn.GetLoanId() != loanID {
		t.Errorf("loan_id = %q, want the id from the path", client.renewIn.GetLoanId())
	}

	if client.renewIn.GetExtendDays() != 7 {
		t.Errorf("extend_days = %d, want 7 from the body", client.renewIn.GetExtendDays())
	}
}

func TestGetRoute(t *testing.T) {
	client := &stubClient{}
	server := newServer(t, client)

	resp := doRequest(t, http.MethodGet, server.URL+"/v1/loans/"+loanID, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if client.getIn.GetLoanId() != loanID {
		t.Errorf("loan_id = %q, want the id from the path", client.getIn.GetLoanId())
	}

	decoded := decode(t, resp)

	if got := decoded["copyId"]; got != copyID {
		t.Errorf("copyId = %v, want %s", got, copyID)
	}
}

func TestListQueryParameters(t *testing.T) {
	client := &stubClient{}
	server := newServer(t, client)

	resp := doRequest(t, http.MethodGet,
		server.URL+"/v1/loans?reader_id="+readerID+"&status=LOAN_STATUS_OVERDUE&limit=5&offset=10", "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if client.listIn.GetReaderId() != readerID {
		t.Errorf("reader_id = %q, want %q", client.listIn.GetReaderId(), readerID)
	}

	if client.listIn.GetStatus() != loanv1.LoanStatus_LOAN_STATUS_OVERDUE {
		t.Errorf("status = %v, want LOAN_STATUS_OVERDUE", client.listIn.GetStatus())
	}

	if client.listIn.GetLimit() != 5 || client.listIn.GetOffset() != 10 {
		t.Errorf("limit/offset = %d/%d, want 5/10", client.listIn.GetLimit(), client.listIn.GetOffset())
	}

	decoded := decode(t, resp)

	if got := decoded["total"]; got != float64(1) {
		t.Errorf("total = %v, want 1", got)
	}
}

func TestListOverdueRoute(t *testing.T) {
	client := &stubClient{}
	server := newServer(t, client)

	resp := doRequest(t, http.MethodGet, server.URL+"/v1/loans/overdue?limit=3", "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if client.overdueIn.GetLimit() != 3 {
		t.Errorf("limit = %d, want 3", client.overdueIn.GetLimit())
	}

	decoded := decode(t, resp)

	loans, ok := decoded["loans"].([]any)
	if !ok || len(loans) != 1 {
		t.Fatalf("loans = %v, want a single-element array", decoded["loans"])
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "not found maps to 404",
			err:      status.Error(codes.NotFound, "loan not found"),
			wantCode: http.StatusNotFound,
		},
		{
			name:     "failed precondition maps to 400",
			err:      status.Error(codes.FailedPrecondition, "loan is already returned"),
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "permission denied maps to 403",
			err:      status.Error(codes.PermissionDenied, "permission denied"),
			wantCode: http.StatusForbidden,
		},
		{
			name:     "unauthenticated maps to 401",
			err:      status.Error(codes.Unauthenticated, "authentication required"),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "invalid argument maps to 400",
			err:      status.Error(codes.InvalidArgument, "malformed loan_id"),
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubClient{err: tc.err}
			server := newServer(t, client)

			resp := doRequest(t, http.MethodGet, server.URL+"/v1/loans/"+loanID, "")

			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}

			decoded := decode(t, resp)

			// The gateway reports the proto code number so the client can branch
			// on it, not only on the HTTP status.
			if got := decoded["code"]; got != float64(status.Code(tc.err)) {
				t.Errorf("error code = %v, want %d", got, status.Code(tc.err))
			}
		})
	}
}

func TestUnknownRouteMapsToNotFound(t *testing.T) {
	server := newServer(t, &stubClient{})

	resp := doRequest(t, http.MethodGet, server.URL+"/v1/unknown", "")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestSwaggerEndpoints(t *testing.T) {
	server := newServer(t, &stubClient{})

	specResp, err := http.Get(server.URL + "/swagger/swagger.json")
	if err != nil {
		t.Fatalf("get swagger.json: %v", err)
	}

	defer specResp.Body.Close()

	if specResp.StatusCode != http.StatusOK {
		t.Fatalf("swagger.json status = %d, want %d", specResp.StatusCode, http.StatusOK)
	}

	if got := specResp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("swagger.json content type = %q, want application/json", got)
	}

	var spec map[string]any
	if err := json.NewDecoder(specResp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode swagger.json: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("swagger.json has no paths section")
	}

	for _, path := range []string{"/v1/loans", "/v1/loans/{loanId}", "/v1/loans/{loanId}/return", "/v1/loans/{loanId}/renew", "/v1/loans/overdue"} {
		if _, ok := paths[path]; !ok {
			t.Errorf("swagger.json is missing path %s", path)
		}
	}

	uiResp, err := http.Get(server.URL + "/swagger/")
	if err != nil {
		t.Fatalf("get /swagger/: %v", err)
	}

	defer uiResp.Body.Close()

	if uiResp.StatusCode != http.StatusOK {
		t.Fatalf("/swagger/ status = %d, want %d", uiResp.StatusCode, http.StatusOK)
	}

	if got := uiResp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("/swagger/ content type = %q, want text/html", got)
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	rootResp, err := noRedirect.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}

	defer rootResp.Body.Close()

	if rootResp.StatusCode != http.StatusFound || rootResp.Header.Get("Location") != "/swagger/" {
		t.Errorf("/ = %d %q, want 302 -> /swagger/", rootResp.StatusCode, rootResp.Header.Get("Location"))
	}
}
