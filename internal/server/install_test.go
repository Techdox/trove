package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/pkg/model"
)

func TestHandleCreateAgentMintsTokenAndSnippet(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, slog.Default())

	body := `{"name":"docker-nas","platform":"docker","server_url":"https://trove.example:8080"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var got createAgentResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "docker-nas" || got.Platform != "docker" {
		t.Fatalf("identity = %+v", got)
	}
	if !strings.HasPrefix(got.Token, "trove_") {
		t.Fatalf("token %q", got.Token)
	}
	if !strings.Contains(got.Snippet, got.Token) || !strings.Contains(got.Snippet, "https://trove.example:8080") {
		t.Fatalf("snippet missing token or server url:\n%s", got.Snippet)
	}
	if got.Filename != "docker-compose.yml" || got.Format != "yaml" {
		t.Fatalf("snippet meta = %+v", got)
	}

	if _, err := st.AuthenticateByToken(t.Context(), got.Token); err != nil {
		t.Fatalf("minted token did not authenticate: %v", err)
	}
}

func TestHandleCreateAgentRejectsDuplicatesAndBadInput(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, slog.Default())

	create := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		return rr
	}

	first := create(`{"name":"docker-nas","platform":"docker","server_url":"http://10.0.0.5:8080"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status = %d", first.Code)
	}
	dup := create(`{"name":"docker-nas","platform":"docker","server_url":"http://10.0.0.5:8080"}`)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d", dup.Code)
	}

	cases := map[string]int{
		`{"name":"bad name","platform":"docker","server_url":"http://10.0.0.5:8080"}`: http.StatusBadRequest,
		`{"name":"ok","platform":"nomad","server_url":"http://10.0.0.5:8080"}`:        http.StatusBadRequest,
		`{"name":"ok","platform":"docker","server_url":"javascript:alert(1)"}`:        http.StatusBadRequest,
		`{"name":"ok","platform":"docker","server_url":"http://user:pass@host"}`:      http.StatusBadRequest,
		`{"name":"ok","platform":"docker","server_url":"http://10.0.0.5:8080"}{}`:     http.StatusBadRequest,
	}
	for body, want := range cases {
		got := create(body)
		if got.Code != want {
			t.Errorf("body %s: status = %d, want %d (%s)", body, got.Code, want, got.Body.String())
		}
	}
}

func TestInstallSnippetCoversEachPlatform(t *testing.T) {
	token := "trove_testtoken"
	url := "http://trove.lan:8080"
	for _, platform := range []model.Platform{
		model.PlatformDocker, model.PlatformKubernetes, model.PlatformProxmox, model.PlatformLocal,
	} {
		snippet, _, _ := installSnippet(platform, url, token)
		if !strings.Contains(snippet, token) || !strings.Contains(snippet, url) {
			t.Errorf("%s snippet missing token or url:\n%s", platform, snippet)
		}
	}
}

func TestSanitizeInstallServerURL(t *testing.T) {
	got, err := sanitizeInstallServerURL(" https://trove.example/ ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://trove.example" {
		t.Fatalf("got %q", got)
	}
	if _, err := sanitizeInstallServerURL("ftp://trove.example"); err == nil {
		t.Fatal("expected error for ftp")
	}
}
