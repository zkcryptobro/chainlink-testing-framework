package framework

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLokiClient_QueryLogs tests the Client's ability to query Loki logs
func TestLokiClient_SuccessfulQuery(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/loki/api/v1/query_range", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{
			"data": {
				"result": [
					{
						"stream": {"namespace": "test"},
						"values": [
							["1234567890", "Log message 1"],
							["1234567891", "Log message 2"]
						]
					}
				]
			}
		}`))
		assert.NoError(t, err)
	}))
	defer mockServer.Close()
	auth := BasicAuth{
		Login:    "test-login",
		Password: "test-password",
	}
	queryParams := QueryParams{
		Query:     `{namespace="test"}`,
		StartTime: time.Now().Add(-1 * time.Hour),
		EndTime:   time.Now(),
		Limit:     100,
	}
	lokiClient := NewLokiQueryClient(mockServer.URL, "test-tenant", auth, queryParams)
	logEntries, err := lokiClient.QueryRange(context.Background())
	assert.NoError(t, err)
	assert.Len(t, logEntries, 2)
	assert.Equal(t, "1234567890", logEntries[0].Timestamp)
	assert.Equal(t, "Log message 1", logEntries[0].Log)
	assert.Equal(t, "1234567891", logEntries[1].Timestamp)
	assert.Equal(t, "Log message 2", logEntries[1].Log)
}

func TestLokiClient_AuthenticationFailure(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/loki/api/v1/query_range", r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized) // Simulate authentication failure
	}))
	defer mockServer.Close()
	auth := BasicAuth{
		Login:    "wrong-login",
		Password: "wrong-password",
	}
	queryParams := QueryParams{
		Query:     `{namespace="test"}`,
		StartTime: time.Now().Add(-1 * time.Hour),
		EndTime:   time.Now(),
		Limit:     100,
	}
	lokiClient := NewLokiQueryClient(mockServer.URL, "test-tenant", auth, queryParams)
	logEntries, err := lokiClient.QueryRange(context.Background())
	assert.Nil(t, logEntries)
	assert.Error(t, err)
	var lokiErr *APIError
	if errors.As(err, &lokiErr) {
		assert.Equal(t, http.StatusUnauthorized, lokiErr.StatusCode)
	} else {
		t.Fatalf("Expected APIError, got %v", err)
	}
}

func TestLokiClient_InternalServerError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/loki/api/v1/query_range", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)                     // Simulate server error
		_, err := w.Write([]byte(`{"message": "internal server error"}`)) // Error message in the response body
		assert.NoError(t, err)
	}))
	defer mockServer.Close()
	auth := BasicAuth{
		Login:    "test-login",
		Password: "test-password",
	}
	queryParams := QueryParams{
		Query:     `{namespace="test"}`,
		StartTime: time.Now().Add(-1 * time.Hour),
		EndTime:   time.Now(),
		Limit:     100,
	}
	lokiClient := NewLokiQueryClient(mockServer.URL, "test-tenant", auth, queryParams)
	logEntries, err := lokiClient.QueryRange(context.Background())
	assert.Nil(t, logEntries)
	assert.Error(t, err)
	var lokiErr *APIError
	if errors.As(err, &lokiErr) {
		assert.Equal(t, http.StatusInternalServerError, lokiErr.StatusCode)
	} else {
		t.Fatalf("Expected APIError, got %v", err)
	}
}

func TestLokiClient_DebugMode(t *testing.T) {
	os.Setenv("RESTY_DEBUG", "true")
	defer os.Unsetenv("RESTY_DEBUG") // Clean up after the test
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/loki/api/v1/query_range", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{
            "data": {
                "result": [
                    {
                        "stream": {"namespace": "test"},
                        "values": [
                            ["1234567890", "Log message 1"],
                            ["1234567891", "Log message 2"]
                        ]
                    }
                ]
            }
        }`))
		assert.NoError(t, err)
	}))
	defer mockServer.Close()
	auth := BasicAuth{
		Login:    "test-login",
		Password: "test-password",
	}
	queryParams := QueryParams{
		Query:     `{namespace="test"}`,
		StartTime: time.Now().Add(-1 * time.Hour),
		EndTime:   time.Now(),
		Limit:     100,
	}
	lokiClient := NewLokiQueryClient(mockServer.URL, "test-tenant", auth, queryParams)
	logEntries, err := lokiClient.QueryRange(context.Background())
	assert.NoError(t, err)
	assert.Len(t, logEntries, 2)
	assert.True(t, lokiClient.RestyClient.Debug)
}
