package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/security"
)

func TestHandleLoginErrorContract(t *testing.T) {
	server := newLoginHandlerTestServer(t, 100)
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: `{}`},
		{name: "missing password", body: `{"username":"admin"}`},
		{name: "missing username", body: `{"password":"secret"}`},
		{name: "blank username", body: `{"username":"  ","password":"secret"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := loginRequest(t, server, tt.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if code := responseErrorCode(t, response); code != "invalid_request" {
				t.Fatalf("error code = %q, want invalid_request", code)
			}
		})
	}
}

func TestHandleLoginDoesNotRevealWhichCredentialFailed(t *testing.T) {
	server := newLoginHandlerTestServer(t, 100)
	unknownUser := loginRequest(t, server, `{"username":"missing","password":"secret"}`)
	wrongPassword := loginRequest(t, server, `{"username":"admin","password":"wrong"}`)
	if unknownUser.Code != http.StatusUnauthorized || wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("statuses = %d and %d, want 401", unknownUser.Code, wrongPassword.Code)
	}
	if unknownUser.Body.String() != wrongPassword.Body.String() {
		t.Fatalf("credential failures differ: %q != %q", unknownUser.Body.String(), wrongPassword.Body.String())
	}
}

func TestHandleLoginRateLimitContract(t *testing.T) {
	server := newLoginHandlerTestServer(t, 1)
	first := loginRequest(t, server, `{"username":"admin","password":"wrong"}`)
	limited := loginRequest(t, server, `{"username":"admin","password":"secret"}`)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first status = %d, want 401", first.Code)
	}
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d, want 429", limited.Code)
	}
	if code := responseErrorCode(t, limited); code != "too_many_requests" {
		t.Fatalf("error code = %q, want too_many_requests", code)
	}
}

func newLoginHandlerTestServer(t *testing.T, rate int) *Server {
	t.Helper()
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	authService, err := auth.NewForTest([]config.User{{Username: "admin", PasswordHash: hash, Role: "admin"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		deps: Deps{
			Cfg:     config.File{Security: config.Security{MaxBodyBytes: 1 << 20}},
			Auth:    authService,
			Observe: observe.New(),
		},
		loginL: security.NewLimiter(rate),
	}
}

func loginRequest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewBufferString(body))
	request.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	server.handleLogin(response, request)
	return response
}

func responseErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code
}
