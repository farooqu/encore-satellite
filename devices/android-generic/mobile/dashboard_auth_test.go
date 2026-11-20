package mobile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardCallbackOnlyAcceptsOneMatchingApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const host = "192.168.77.2:49152"
	const callback = "http://" + host + "/approved?state=expected&code=test-code"
	codes := make(chan string, 1)
	handler := dashboardCallback(ctx, host, "expected", codes)

	for _, tc := range []struct {
		name, method, url string
		status            int
	}{
		{"different host", "GET", "http://other.example/approved?state=expected&code=test-code", 404},
		{"not a navigation", "POST", callback, 404},
		{"wrong state", "GET", "http://" + host + "/approved?state=wrong&code=test-code", 400},
		{"duplicate state", "GET", callback + "&state=expected", 400},
		{"approved", "GET", callback, 303},
		{"replayed", "GET", callback, 410},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.url, nil))
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status = %d, headers = %v", response.Code, response.Header())
			}
			if tc.status == http.StatusSeeOther {
				if response.Header().Get("Location") != "/done" || len(codes) != 1 || <-codes != "test-code" {
					t.Fatal("callback didn't deliver the code once with a sanitized redirect")
				}
			} else if len(codes) != 0 {
				t.Fatal("invalid or replayed callback delivered a code")
			}
		})
	}
	cancel()
	response := httptest.NewRecorder()
	dashboardCallback(ctx, host, "expected", codes).ServeHTTP(response, httptest.NewRequest("GET", callback, nil))
	if response.Code != http.StatusBadRequest || len(codes) != 0 {
		t.Fatal("expired enrollment accepted a matching code")
	}
}
