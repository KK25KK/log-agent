package aliyunsls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	sls "github.com/aliyun/aliyun-log-go-sdk"
)

const (
	ecsMetadataRoot      = "http://100.100.100.200/"
	ecsMetadataTokenTTL  = "21600"
	maxMetadataBody      = 64 * 1024
	maxMetadataTokenBody = 4 * 1024
	credentialFetchAhead = 2 * time.Minute
)

var ecsRoleNamePattern = regexp.MustCompile(`^[A-Za-z0-9.@_-]{1,64}$`)

// ecsRAMRoleProvider supplies the SDK interface while bounding metadata HTTP
// calls. The upstream v0.1.126 provider uses an http.Client without a timeout.
type ecsRAMRoleProvider struct {
	mu             sync.Mutex
	roleName       string
	client         *http.Client
	metadataRoot   string
	requestTimeout time.Duration
	now            func() time.Time

	credentials sls.Credentials
	expiration  time.Time
}

type ecsMetadataResponse struct {
	Code            string    `json:"Code"`
	AccessKeyID     string    `json:"AccessKeyId"`
	AccessKeySecret string    `json:"AccessKeySecret"`
	SecurityToken   string    `json:"SecurityToken"`
	Expiration      time.Time `json:"Expiration"`
}

func newECSRAMRoleProvider(roleName string, timeout time.Duration) (sls.CredentialsProvider, error) {
	if !ecsRoleNamePattern.MatchString(roleName) {
		return nil, errors.New("ECS RAM role name must contain 1-64 safe characters")
	}
	if timeout <= 0 {
		return nil, errors.New("ECS credential request timeout must be positive")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Link-local ECS metadata must never be routed through a process-wide proxy.
	transport.Proxy = nil
	return &ecsRAMRoleProvider{
		roleName: roleName,
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("ECS metadata redirects are disabled")
			},
		},
		metadataRoot:   ecsMetadataRoot,
		requestTimeout: timeout,
		now:            time.Now,
	}, nil
}

func (p *ecsRAMRoleProvider) GetCredentials() (sls.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now().UTC()
	if p.credentials.AccessKeyID != "" && now.Add(credentialFetchAhead).Before(p.expiration) {
		return p.credentials, nil
	}

	credentials, expiration, err := p.fetch(now)
	if err != nil {
		// A still-valid cached credential is safer than turning a transient
		// metadata outage into an immediate query outage.
		if p.credentials.AccessKeyID != "" && now.Before(p.expiration) {
			return p.credentials, nil
		}
		return sls.Credentials{}, err
	}
	p.credentials = credentials
	p.expiration = expiration
	return credentials, nil
}

func (p *ecsRAMRoleProvider) fetch(now time.Time) (sls.Credentials, time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.requestTimeout)
	defer cancel()

	token, err := p.fetchMetadataToken(ctx)
	if err != nil {
		return sls.Credentials{}, time.Time{}, err
	}
	endpoint, err := url.JoinPath(p.metadataRoot, "latest/meta-data/ram/security-credentials", p.roleName)
	if err != nil {
		return sls.Credentials{}, time.Time{}, errors.New("build ECS metadata request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return sls.Credentials{}, time.Time{}, errors.New("build ECS metadata request")
	}
	request.Header.Set("X-aliyun-ecs-metadata-token", token)
	response, err := p.client.Do(request)
	if err != nil {
		return sls.Credentials{}, time.Time{}, errors.New("fetch ECS RAM role credentials")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sls.Credentials{}, time.Time{}, fmt.Errorf("fetch ECS RAM role credentials: HTTP %d", response.StatusCode)
	}

	body, err := readBounded(response.Body, maxMetadataBody)
	if err != nil {
		return sls.Credentials{}, time.Time{}, errors.New("read ECS RAM role credentials")
	}
	var payload ecsMetadataResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return sls.Credentials{}, time.Time{}, errors.New("decode ECS RAM role credentials")
	}
	if !strings.EqualFold(payload.Code, "success") || payload.AccessKeyID == "" || payload.AccessKeySecret == "" || payload.SecurityToken == "" || !payload.Expiration.After(now) {
		return sls.Credentials{}, time.Time{}, errors.New("ECS RAM role credential response is invalid")
	}
	return sls.Credentials{
		AccessKeyID:     payload.AccessKeyID,
		AccessKeySecret: payload.AccessKeySecret,
		SecurityToken:   payload.SecurityToken,
	}, payload.Expiration.UTC(), nil
}

func (p *ecsRAMRoleProvider) fetchMetadataToken(ctx context.Context) (string, error) {
	endpoint, err := url.JoinPath(p.metadataRoot, "latest/api/token")
	if err != nil {
		return "", errors.New("build ECS metadata token request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return "", errors.New("build ECS metadata token request")
	}
	request.Header.Set("X-aliyun-ecs-metadata-token-ttl-seconds", ecsMetadataTokenTTL)
	response, err := p.client.Do(request)
	if err != nil {
		return "", errors.New("fetch ECS metadata token")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("fetch ECS metadata token: HTTP %d", response.StatusCode)
	}
	body, err := readBounded(response.Body, maxMetadataTokenBody)
	if err != nil {
		return "", errors.New("read ECS metadata token")
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return "", errors.New("ECS metadata token response is invalid")
	}
	return token, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return body, nil
}

var _ sls.CredentialsProvider = (*ecsRAMRoleProvider)(nil)
