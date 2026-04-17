package otelingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const (
	contentTypeProtobuf = "application/x-protobuf"
	contentTypeJSON     = "application/json"
)

// Server is an in-operator OTLP/HTTP receiver for the RCA correlation pipeline.
//
// It implements sigs.k8s.io/controller-runtime/pkg/manager.Runnable so it can
// be registered with the same lifecycle as the dashboard and incident engine.
//
// Milestone A scope: receive and translate only — no new rules, no downstream
// graph/insight behavior. Filtered signals join the shared correlator channel
// through the existing watcher.EventEmitter.
type Server struct {
	cfg        Config
	translator *Translator
	log        logr.Logger
	httpSrv    *http.Server
}

// NewServer builds a Server wired to the shared EventEmitter.
func NewServer(cfg Config, emitter watcher.EventEmitter, logger logr.Logger) *Server {
	applyConfigDefaults(&cfg)
	s := &Server{
		cfg:        cfg,
		translator: NewTranslator(cfg, emitter),
		log:        logger.WithName("otel-ingest"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", s.handleTraces)
	mux.HandleFunc("/v1/logs", s.handleLogs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	s.httpSrv = &http.Server{
		Addr:              cfg.BindAddress,
		Handler:           mux,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Start satisfies manager.Runnable. It blocks until ctx is canceled and returns
// any server error other than http.ErrServerClosed.
func (s *Server) Start(ctx context.Context) error {
	s.log.Info("Starting OTLP ingest server", "bindAddress", s.cfg.BindAddress)
	listener, err := net.Listen("tcp", s.cfg.BindAddress)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// NeedLeaderElection returns false: OTLP ingest should run on every replica so
// collectors can fan out traffic without leader-election-aware routing. The
// shared correlator channel is per-pod, so duplicates are not an issue at
// this stage (the correlator's dedup key handles collisions).
func (s *Server) NeedLeaderElection() bool { return false }

// handleTraces decodes an OTLP ExportTraceServiceRequest and forwards its
// ResourceSpans to the translator. Responds with an empty
// ExportTraceServiceResponse (partial_success unset = full accept).
func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := readLimited(r, s.cfg.MaxRequestBytes)
	if err != nil {
		http.Error(w, err.Error(), statusForError(err))
		return
	}
	req := &coltracepb.ExportTraceServiceRequest{}
	if err := decodeOTLP(r.Header.Get("Content-Type"), body, req); err != nil {
		s.log.Info("Rejected OTLP trace request", "reason", err.Error(), "contentType", r.Header.Get("Content-Type"))
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	emitted := s.translator.TranslateResourceSpans(req.GetResourceSpans())
	s.log.V(1).Info("Accepted OTLP trace batch", "resourceSpans", len(req.GetResourceSpans()), "emittedEvents", emitted)
	writeOTLPResponse(w, r.Header.Get("Content-Type"), &coltracepb.ExportTraceServiceResponse{})
}

// handleLogs decodes an OTLP ExportLogsServiceRequest and forwards its
// ResourceLogs to the translator.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := readLimited(r, s.cfg.MaxRequestBytes)
	if err != nil {
		http.Error(w, err.Error(), statusForError(err))
		return
	}
	req := &collogspb.ExportLogsServiceRequest{}
	if err := decodeOTLP(r.Header.Get("Content-Type"), body, req); err != nil {
		s.log.Info("Rejected OTLP log request", "reason", err.Error(), "contentType", r.Header.Get("Content-Type"))
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	emitted := s.translator.TranslateResourceLogs(req.GetResourceLogs())
	s.log.V(1).Info("Accepted OTLP log batch", "resourceLogs", len(req.GetResourceLogs()), "emittedEvents", emitted)
	writeOTLPResponse(w, r.Header.Get("Content-Type"), &collogspb.ExportLogsServiceResponse{})
}

// ---- helpers ----------------------------------------------------------------

type oversizedError struct{ limit int64 }

func (e *oversizedError) Error() string { return "request exceeds configured max size" }

func readLimited(r *http.Request, max int64) ([]byte, error) {
	if max <= 0 {
		max = 16 * 1024 * 1024
	}
	reader := http.MaxBytesReader(nil, r.Body, max)
	defer func() { _ = r.Body.Close() }()
	rawBody, err := io.ReadAll(reader)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &oversizedError{limit: max}
		}
		return nil, err
	}
	body, err := decodeContentEncoding(r.Header.Get("Content-Encoding"), rawBody, max)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func decodeContentEncoding(contentEncoding string, body []byte, max int64) ([]byte, error) {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	switch encoding {
	case "", "identity":
		return body, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer func() { _ = zr.Close() }()

		// Guard against gzip bombs on decompressed payload size.
		limited := io.LimitReader(zr, max+1)
		decoded, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if int64(len(decoded)) > max {
			return nil, &oversizedError{limit: max}
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported content encoding: %s", contentEncoding)
	}
}

func statusForError(err error) int {
	var ose *oversizedError
	if errors.As(err, &ose) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// decodeOTLP dispatches on Content-Type. Unknown content types default to
// protobuf (the OTLP/HTTP default) which matches collector behavior.
func decodeOTLP(contentType string, body []byte, msg proto.Message) error {
	if isJSONContentType(contentType) {
		return protojson.Unmarshal(body, msg)
	}
	return proto.Unmarshal(body, msg)
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return mediaType == contentTypeJSON
	}
	return strings.EqualFold(strings.TrimSpace(contentType), contentTypeJSON)
}

func writeOTLPResponse(w http.ResponseWriter, reqContentType string, msg proto.Message) {
	if isJSONContentType(reqContentType) {
		data, err := protojson.Marshal(msg)
		if err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentTypeJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentTypeProtobuf)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// applyConfigDefaults fills in zero-valued fields with DefaultConfig values so
// callers can pass a partially-populated Config.
func applyConfigDefaults(cfg *Config) {
	d := DefaultConfig()
	if cfg.BindAddress == "" {
		cfg.BindAddress = d.BindAddress
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = d.ReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = d.WriteTimeout
	}
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = d.MaxRequestBytes
	}
	if cfg.AgentName == "" {
		cfg.AgentName = d.AgentName
	}
}
