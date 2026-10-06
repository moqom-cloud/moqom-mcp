package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBindAddressDefaultsToLoopback(t *testing.T) {
	cases := map[string]string{
		":8765":        "127.0.0.1:8765",
		"8765":         "127.0.0.1:8765",
		"0.0.0.0:8765": "0.0.0.0:8765",
		"[::1]:8765":   "[::1]:8765",
		"localhost:80": "localhost:80",
	}
	for in, want := range cases {
		got, err := bindAddress(in)
		if err != nil || got != want {
			t.Errorf("bindAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRequireBearer(t *testing.T) {
	handler := requireBearer("s3cret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	cases := map[string]int{
		"":               http.StatusUnauthorized,
		"Bearer wrong":   http.StatusUnauthorized,
		"s3cret":         http.StatusUnauthorized,
		"Bearer s3cret":  http.StatusTeapot,
		"Bearer s3cret ": http.StatusUnauthorized,
	}
	for header, want := range cases {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Errorf("Authorization %q: %d, want %d", header, recorder.Code, want)
		}
	}
}
