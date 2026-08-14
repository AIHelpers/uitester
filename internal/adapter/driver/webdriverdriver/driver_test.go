package webdriverdriver_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"uitester/internal/adapter/driver/webdriverdriver"
)

// newFakeWebDriverServer spins up a minimal, in-memory server that speaks
// just enough of the W3C WebDriver wire protocol to exercise every method
// on webdriverdriver.Driver. This lets the adapter be tested end-to-end
// (real HTTP, real JSON) without depending on a live Selenium/Appium/
// WinAppDriver instance, which won't be available in CI or this sandbox.
func newFakeWebDriverServer(t *testing.T) *httptest.Server {
	t.Helper()
	const sessionID = "sess-123"
	const elementID = "elem-abc"

	mux := http.NewServeMux()

	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"value": map[string]string{"sessionId": sessionID},
		})
	})

	mux.HandleFunc("/session/"+sessionID+"/url", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.URL == "" {
			http.Error(w, `{"value":{"error":"invalid argument"}}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"value": nil})
	})

	mux.HandleFunc("/session/"+sessionID+"/element", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"value": map[string]string{"element-6066-11e4-a52e-4f735466cecf": elementID},
		})
	})

	mux.HandleFunc("/session/"+sessionID+"/element/"+elementID+"/click", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"value": nil})
	})

	mux.HandleFunc("/session/"+sessionID+"/element/"+elementID+"/clear", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"value": nil})
	})

	var lastTypedText string
	mux.HandleFunc("/session/"+sessionID+"/element/"+elementID+"/value", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastTypedText = body.Text
		writeJSON(w, map[string]interface{}{"value": nil})
	})

	mux.HandleFunc("/session/"+sessionID+"/element/"+elementID+"/text", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"value": lastTypedText})
	})

	mux.HandleFunc("/session/"+sessionID+"/screenshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"value": base64.StdEncoding.EncodeToString([]byte("fake-png-bytes")),
		})
	})

	mux.HandleFunc("/session/"+sessionID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			writeJSON(w, map[string]interface{}{"value": nil})
			return
		}
		http.NotFound(w, r)
	})

	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestWebDriverDriver_FullLifecycle(t *testing.T) {
	server := newFakeWebDriverServer(t)
	defer server.Close()

	d := webdriverdriver.New(webdriverdriver.Config{
		RemoteURL:    server.URL,
		Capabilities: map[string]interface{}{"browserName": "chrome"},
	})

	ctx := context.Background()

	if err := d.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := d.Navigate(ctx, "https://example.com"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if err := d.Input(ctx, "#username", "ada"); err != nil {
		t.Fatalf("Input: %v", err)
	}

	got, err := d.GetText(ctx, "#username")
	if err != nil {
		t.Fatalf("GetText: %v", err)
	}
	if got != "ada" {
		t.Fatalf("expected text %q, got %q", "ada", got)
	}

	if err := d.Click(ctx, "#submit"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	if err := d.WaitFor(ctx, "#username", 2*time.Second); err != nil {
		t.Fatalf("WaitFor: %v", err)
	}

	shot, err := d.Screenshot(ctx)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if string(shot) != "fake-png-bytes" {
		t.Fatalf("unexpected screenshot bytes: %q", string(shot))
	}

	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWebDriverDriver_RequiresRemoteURL(t *testing.T) {
	d := webdriverdriver.New(webdriverdriver.Config{})
	if err := d.Connect(context.Background()); err == nil {
		t.Fatal("expected error connecting without a remote_url, got nil")
	}
}

func TestWebDriverDriver_WaitForTimesOutOnMissingElement(t *testing.T) {
	server := newFakeWebDriverServer(t)
	defer server.Close()

	// Point findElement at a path that always 404s to simulate "element never appears".
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"value": map[string]string{"sessionId": "s1"}})
	})
	mux.HandleFunc("/session/s1/element", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"value":{"error":"no such element"}}`, http.StatusNotFound)
	})
	notFoundServer := httptest.NewServer(mux)
	defer notFoundServer.Close()

	d := webdriverdriver.New(webdriverdriver.Config{RemoteURL: notFoundServer.URL})
	if err := d.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	err := d.WaitFor(context.Background(), "#never-appears", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
