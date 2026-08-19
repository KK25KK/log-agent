package aliyunsls

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestECSRAMRoleProviderCachesCredentials(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/latest/api/token":
			if request.Method != http.MethodPut || request.Header.Get("X-aliyun-ecs-metadata-token-ttl-seconds") != ecsMetadataTokenTTL {
				t.Errorf("invalid IMDSv2 token request: method=%s headers=%v", request.Method, request.Header)
			}
			fmt.Fprint(response, "metadata-token")
		case "/latest/meta-data/ram/security-credentials/test-role":
			if request.Method != http.MethodGet || request.Header.Get("X-aliyun-ecs-metadata-token") != "metadata-token" {
				t.Errorf("invalid IMDSv2 credential request: method=%s headers=%v", request.Method, request.Header)
			}
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(response, `{"Code":"Success","AccessKeyId":"temporary-id","AccessKeySecret":"temporary-secret","SecurityToken":"temporary-token","Expiration":%q}`, now.Add(time.Hour).Format(time.RFC3339))
		default:
			t.Errorf("unexpected metadata path: %s", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := testECSProvider(server, now)
	for range 2 {
		credentials, err := provider.GetCredentials()
		if err != nil {
			t.Fatal(err)
		}
		if credentials.AccessKeyID != "temporary-id" || credentials.SecurityToken != "temporary-token" {
			t.Fatalf("unexpected credentials: %#v", credentials)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("credentials were not cached: calls=%d", calls.Load())
	}
}

func TestECSRAMRoleProviderBoundsMetadataRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	provider := testECSProvider(server, time.Now())
	provider.client.Timeout = 10 * time.Millisecond
	provider.requestTimeout = 10 * time.Millisecond
	started := time.Now()
	_, err := provider.GetCredentials()
	if err == nil {
		t.Fatal("want metadata timeout")
	}
	if time.Since(started) > 80*time.Millisecond {
		t.Fatalf("metadata request was not bounded: %s", time.Since(started))
	}
}

func TestECSRAMRoleProviderDoesNotExposeResponseSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/latest/api/token" {
			fmt.Fprint(response, "metadata-token")
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(response, `{"AccessKeySecret":"must-not-escape","SecurityToken":"token-must-not-escape"}`)
	}))
	defer server.Close()

	provider := testECSProvider(server, time.Now())
	_, err := provider.GetCredentials()
	if err == nil {
		t.Fatal("want invalid metadata response")
	}
	if strings.Contains(err.Error(), "must-not-escape") {
		t.Fatalf("metadata secret escaped through error: %v", err)
	}
}

func TestECSRAMRoleProviderRejectsUnsafeRoleName(t *testing.T) {
	if _, err := newECSRAMRoleProvider("../admin?token=secret", time.Second); err == nil {
		t.Fatal("want unsafe role-name error")
	}
}

func testECSProvider(server *httptest.Server, now time.Time) *ecsRAMRoleProvider {
	return &ecsRAMRoleProvider{
		roleName:       "test-role",
		client:         &http.Client{Timeout: time.Second},
		metadataRoot:   server.URL + "/",
		requestTimeout: time.Second,
		now:            func() time.Time { return now },
	}
}
