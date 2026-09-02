package localweb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"logagent/internal/command"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

const maxRequestBodyBytes = 8 * 1024

var (
	logicalNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	requestIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
)

type investigationStore interface {
	GetInvestigation(ctx context.Context, investigationID string) (domain.Investigation, error)
	GetInteractionTarget(ctx context.Context, investigationID string) (domain.InteractionTarget, error)
}

type accepter interface {
	Accept(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error)
}

type actionHandler interface {
	Handle(ctx context.Context, command domain.ActionCommand) (domain.ActionResult, error)
}

type intentHandler interface {
	Resolve(ctx context.Context, inbound domain.InboundMessage, problem string) (domain.IntentResolution, bool, error)
	Confirm(ctx context.Context, resolutionID string, principal domain.Principal, chatID string) (string, bool, error)
	Get(ctx context.Context, resolutionID string, principal domain.Principal) (domain.IntentResolution, error)
	Capabilities(ctx context.Context, principal domain.Principal) ([]domain.InvestigationCapability, error)
}

type Options struct {
	Address        string
	Principal      domain.Principal
	ChatID         string
	IngestionGrace time.Duration
	MaxWindow      time.Duration
	SLSMode        string
	LLMMode        string
	IntentMode     string
}

type ServerOption func(*Server) error

func WithIntentHandler(handler intentHandler) ServerOption {
	return func(server *Server) error {
		if handler == nil {
			return errors.New("local Web intent handler is required")
		}
		server.intents = handler
		return nil
	}
}

// Server translates local HTTP requests into the same application contracts as
// the Feishu adapter. It never accepts identity or physical resource fields.
type Server struct {
	options Options
	store   investigationStore
	intake  accepter
	actions actionHandler
	sender  *Sender
	intents intentHandler
	csrf    string
	handler http.Handler
	now     func() time.Time
}

func NewServer(options Options, store investigationStore, intake accepter, actions actionHandler, sender *Sender, serverOptions ...ServerOption) (*Server, error) {
	if options.IntentMode == "" {
		options.IntentMode = "disabled"
	}
	if err := ValidateLoopbackAddress(options.Address); err != nil {
		return nil, err
	}
	if !options.Principal.Complete() || options.ChatID == "" {
		return nil, errors.New("local Web fixed principal and chat ID are required")
	}
	if options.IngestionGrace < domain.MinimumIngestionGrace {
		return nil, fmt.Errorf("local Web ingestion grace must be at least %s", domain.MinimumIngestionGrace)
	}
	if options.MaxWindow <= 0 {
		return nil, errors.New("local Web maximum investigation window must be positive")
	}
	if store == nil || intake == nil || actions == nil || sender == nil {
		return nil, errors.New("local Web store, intake, actions, and sender are required")
	}
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("create local Web session token: %w", err)
	}
	server := &Server{
		options: options, store: store, intake: intake, actions: actions,
		sender: sender, csrf: token, now: time.Now,
	}
	for _, option := range serverOptions {
		if option == nil {
			return nil, errors.New("local Web server option is nil")
		}
		if err := option(server); err != nil {
			return nil, err
		}
	}
	if options.IntentMode != "disabled" && server.intents == nil {
		return nil, errors.New("local Web intent handler is required when intent mode is enabled")
	}
	server.handler = server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func ValidateLoopbackAddress(address string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("LOG_AGENT_WEB_ADDR must be an IP host and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("LOG_AGENT_WEB_ADDR must use a literal loopback IP")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("LOG_AGENT_WEB_ADDR port must be between 1 and 65535")
	}
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	mux.HandleFunc("GET /app.css", s.handleCSS)
	mux.HandleFunc("GET /app.js", s.handleJS)
	mux.HandleFunc("GET /favicon.ico", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	if s.intents != nil {
		mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
		mux.HandleFunc("POST /api/intents", s.handleResolveIntent)
		mux.HandleFunc("GET /api/intents/{id}", s.handleGetIntent)
		mux.HandleFunc("POST /api/intents/{id}/confirm", s.handleConfirmIntent)
	}
	mux.HandleFunc("POST /api/investigations", s.handleSubmit)
	mux.HandleFunc("GET /api/investigations/{id}", s.handleGet)
	mux.HandleFunc("POST /api/investigations/{id}/actions", s.handleAction)
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != s.options.Address {
			http.Error(writer, "invalid local host", http.StatusMisdirectedRequest)
			return
		}
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) handlePage(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(writer, pageHTML(s.csrf))
}

func (s *Server) handleCSS(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = io.WriteString(writer, "[hidden]{display:none!important}"+appCSS)
}

func (s *Server) handleJS(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = io.WriteString(writer, appJS)
}

func (s *Server) handleMeta(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"sls_mode": s.options.SLSMode, "llm_mode": s.options.LLMMode, "intent_mode": s.options.IntentMode,
		"feishu_mode": "mock", "identity_source": "server_fixed",
		"warning": "本页验证 Agent 应用链路，不代表真实飞书链路已验收。",
	})
}

func (s *Server) handleCapabilities(writer http.ResponseWriter, request *http.Request) {
	capabilities, err := s.intents.Capabilities(request.Context(), s.options.Principal)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "capability_failed", "logical capabilities could not be loaded")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"capabilities": capabilities})
}

type resolveIntentRequest struct {
	RequestID string `json:"request_id"`
	Problem   string `json:"problem"`
}

func (s *Server) handleResolveIntent(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	var input resolveIntentRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !requestIDPattern.MatchString(input.RequestID) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_id", "request ID is invalid")
		return
	}
	now := s.now().UTC()
	messageID := "web-intent:" + input.RequestID
	resolution, created, err := s.intents.Resolve(request.Context(), domain.InboundMessage{
		AppID: s.options.Principal.AppID, TenantKey: s.options.Principal.TenantKey,
		MessageID: messageID, ReplyToMessageID: messageID, ChatID: s.options.ChatID,
		UserID: s.options.Principal.UserID, Text: input.Problem, ReceivedAt: now,
	}, input.Problem)
	if errors.Is(err, ports.ErrIntentInvalid) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_problem", "problem description is empty, unsafe, or exceeds the configured limit")
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "intent_failed", "problem description could not be parsed")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, struct {
		Created    bool                    `json:"created"`
		Resolution domain.IntentResolution `json:"resolution"`
	}{Created: created, Resolution: resolution})
}

func (s *Server) handleGetIntent(writer http.ResponseWriter, request *http.Request) {
	resolutionID := request.PathValue("id")
	if !validIntentResolutionID(resolutionID) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_id", "intent resolution ID is invalid")
		return
	}
	resolution, err := s.intents.Get(request.Context(), resolutionID, s.options.Principal)
	if errors.Is(err, ports.ErrNotFound) {
		writeAPIError(writer, http.StatusNotFound, "not_found", "intent resolution was not found")
		return
	}
	if errors.Is(err, ports.ErrIntentForbidden) {
		writeAPIError(writer, http.StatusForbidden, "intent_forbidden", "intent resolution is not authorized")
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "intent_load_failed", "intent resolution could not be loaded")
		return
	}
	writeJSON(writer, http.StatusOK, resolution)
}

type confirmIntentRequest struct {
	RequestID string `json:"request_id"`
}

func (s *Server) handleConfirmIntent(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	var input confirmIntentRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !requestIDPattern.MatchString(input.RequestID) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_id", "request ID is invalid")
		return
	}
	resolutionID := request.PathValue("id")
	if !validIntentResolutionID(resolutionID) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_id", "intent resolution ID is invalid")
		return
	}
	investigationID, created, err := s.intents.Confirm(request.Context(), resolutionID, s.options.Principal, s.options.ChatID)
	switch {
	case errors.Is(err, ports.ErrNotFound):
		writeAPIError(writer, http.StatusNotFound, "not_found", "intent resolution was not found")
		return
	case errors.Is(err, ports.ErrIntentForbidden):
		writeAPIError(writer, http.StatusForbidden, "intent_forbidden", "intent resolution is not authorized")
		return
	case errors.Is(err, ports.ErrIntentExpired):
		writeAPIError(writer, http.StatusConflict, "intent_expired", "intent resolution has expired; parse the problem again")
		return
	case errors.Is(err, ports.ErrIntentInvalid):
		writeAPIError(writer, http.StatusConflict, "intent_not_confirmable", "intent resolution is not safe to confirm")
		return
	case err != nil:
		writeAPIError(writer, http.StatusInternalServerError, "intent_confirm_failed", "intent resolution could not be confirmed")
		return
	}
	view, err := s.loadView(request.Context(), investigationID)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "load_failed", "investigation was accepted but could not be loaded")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, struct {
		Created       bool              `json:"created"`
		Investigation InvestigationView `json:"investigation"`
	}{Created: created, Investigation: view})
}

type submitRequest struct {
	RequestID   string `json:"request_id"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Duration    string `json:"duration"`
	TemplateID  string `json:"template_id,omitempty"`
}

func (s *Server) handleSubmit(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	var input submitRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !requestIDPattern.MatchString(input.RequestID) || !logicalNamePattern.MatchString(input.Service) || !logicalNamePattern.MatchString(input.Environment) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_scope", "request ID, service, or environment is invalid")
		return
	}
	commandText := fmt.Sprintf("/investigate %s %s %s", input.Service, input.Environment, input.Duration)
	if input.TemplateID != "" {
		commandText += " " + input.TemplateID
	}
	now := s.now().UTC()
	normalized, err := command.ParseInvestigationWithGrace(commandText, now, s.options.IngestionGrace)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_investigation", "service, environment, duration, or template is invalid")
		return
	}
	if normalized.EndTime.Sub(normalized.StartTime) > s.options.MaxWindow {
		writeAPIError(writer, http.StatusBadRequest, "window_exceeds_policy", "duration exceeds the configured investigation policy")
		return
	}
	messageID := "web:" + input.RequestID
	investigationID, created, err := s.intake.Accept(request.Context(), domain.InboundMessage{
		AppID: s.options.Principal.AppID, TenantKey: s.options.Principal.TenantKey,
		MessageID: messageID, ReplyToMessageID: messageID, ChatID: s.options.ChatID,
		UserID: s.options.Principal.UserID, Text: commandText, ReceivedAt: now,
	}, normalized)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "intake_failed", "investigation could not be accepted")
		return
	}
	view, err := s.loadView(request.Context(), investigationID)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "load_failed", "investigation was accepted but could not be loaded")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, struct {
		Created       bool              `json:"created"`
		Investigation InvestigationView `json:"investigation"`
	}{Created: created, Investigation: view})
}

func (s *Server) handleGet(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if !validInvestigationID(id) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_id", "investigation ID is invalid")
		return
	}
	view, err := s.loadView(request.Context(), id)
	if errors.Is(err, ports.ErrNotFound) {
		writeAPIError(writer, http.StatusNotFound, "not_found", "investigation was not found")
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "load_failed", "investigation could not be loaded")
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

type actionRequest struct {
	RequestID string                     `json:"request_id"`
	Action    domain.InvestigationAction `json:"action"`
}

func (s *Server) handleAction(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	id := request.PathValue("id")
	if !validInvestigationID(id) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_id", "investigation ID is invalid")
		return
	}
	var input actionRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !requestIDPattern.MatchString(input.RequestID) || !allowedAction(input.Action) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_action", "request ID or action is invalid")
		return
	}
	target, err := s.store.GetInteractionTarget(request.Context(), id)
	if errors.Is(err, ports.ErrNotFound) || (err == nil && target.CardMessageID == "") {
		writeAPIError(writer, http.StatusConflict, "delivery_not_ready", "the local interaction projection is not ready")
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "interaction_failed", "interaction target could not be loaded")
		return
	}
	result, err := s.actions.Handle(request.Context(), domain.ActionCommand{
		EventID: "web:" + input.RequestID, Action: input.Action, InvestigationID: id,
		Principal: s.options.Principal, ChatID: s.options.ChatID,
		CardMessageID: target.CardMessageID, OccurredAt: s.now().UTC(),
	})
	if errors.Is(err, ports.ErrActionForbidden) {
		writeAPIError(writer, http.StatusForbidden, "action_forbidden", "the action is not authorized")
		return
	}
	if errors.Is(err, ports.ErrActionInvalid) {
		writeAPIError(writer, http.StatusConflict, "action_conflict", "the action is not valid for the current state")
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "action_failed", "the action could not be completed")
		return
	}
	view, err := s.loadView(request.Context(), result.Investigation.ID)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, "load_failed", "the updated investigation could not be loaded")
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		View          domain.ActionView `json:"view"`
		Created       bool              `json:"created"`
		Investigation InvestigationView `json:"investigation"`
	}{View: result.View, Created: result.Created, Investigation: view})
}

func (s *Server) authorizeMutation(writer http.ResponseWriter, request *http.Request) bool {
	if request.Header.Get("X-Log-Agent-CSRF") != s.csrf {
		writeAPIError(writer, http.StatusForbidden, "csrf_rejected", "local session token is missing or invalid")
		return false
	}
	if origin := request.Header.Get("Origin"); origin != "" && origin != "http://"+s.options.Address {
		writeAPIError(writer, http.StatusForbidden, "origin_rejected", "request origin is not local")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(writer, http.StatusUnsupportedMediaType, "content_type_rejected", "Content-Type must be application/json")
		return false
	}
	return true
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request body must be one valid JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must not contain trailing data")
	}
	return nil
}

func allowedAction(action domain.InvestigationAction) bool {
	switch action {
	case domain.ActionViewEvidence, domain.ActionViewReport, domain.ActionCancel,
		domain.ActionExpandWindow, domain.ActionRerun, domain.ActionRerunWithCostAck:
		return true
	default:
		return false
	}
}

func validInvestigationID(id string) bool {
	return strings.HasPrefix(id, "inv_") && len(id) <= 128 && requestIDPattern.MatchString(id)
}

func validIntentResolutionID(id string) bool {
	return strings.HasPrefix(id, "intent_") && len(id) <= 128 && requestIDPattern.MatchString(id)
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeAPIError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}
