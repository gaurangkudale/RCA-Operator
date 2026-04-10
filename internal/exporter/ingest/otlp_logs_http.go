package ingest

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// OTLP/HTTP support.
//
// The OpenTelemetry Protocol defines two equivalent transports for log
// records: OTLP/gRPC and OTLP/HTTP. The HTTP variant is documented at
// https://opentelemetry.io/docs/specs/otlp/#otlphttp and used by:
//
//   - The OTel Collector's `otlphttp` exporter
//   - Browser/JS SDKs (which cannot speak gRPC)
//   - Lightweight forwarders that prefer plain HTTP for firewall traversal
//
// The wire contract:
//
//   - URL path: /v1/logs
//   - Method:   POST
//   - Body:     ExportLogsServiceRequest serialized as either
//                 * application/x-protobuf  (binary protobuf, default)
//                 * application/json        (canonical protojson)
//   - Optional Content-Encoding: gzip
//   - Response: 200 OK with empty ExportLogsServiceResponse on success
//
// Both encodings deserialize into the same generated proto type, so the
// HTTP handler reuses the gRPC LogsReceiver.Export method verbatim — there
// is no separate aggregation path. This guarantees the two transports stay
// behaviourally identical and only one set of tests is needed for the
// detection logic.

// otlpHTTPLogsPath is the canonical OTLP/HTTP logs URL path. Hard-coded
// because the OTLP spec fixes it; clients send to /v1/logs unconditionally.
const otlpHTTPLogsPath = "/v1/logs"

// httpMaxRequestBytes caps the size of a single OTLP/HTTP request body to
// protect the exporter from OOMs caused by an upstream collector with a
// misconfigured batch size. 16 MiB is far above any reasonable single OTLP
// batch (the collector's default is ~512 KiB) but well under the
// memory.limits we set in config/rca-exporter/deployment.yaml.
const httpMaxRequestBytes = 16 << 20

// HTTPHandler returns an http.Handler that accepts OTLP/HTTP log requests
// and dispatches them to the supplied LogsReceiver. The handler is mounted
// at /v1/logs by Serve below; tests construct it directly via
// NewHTTPHandler so they can exercise the full request lifecycle (encoding
// negotiation, gzip, error responses) without binding a real port.
func NewHTTPHandler(recv *LogsReceiver) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(otlpHTTPLogsPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := readRequestBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		req := &collogspb.ExportLogsServiceRequest{}
		ct := contentType(r.Header.Get("Content-Type"))
		switch ct {
		case "application/x-protobuf", "application/protobuf", "":
			// Empty content-type defaults to protobuf — matches what the
			// OTel Collector's otlphttp exporter sends when the operator
			// forgets to set encoding explicitly.
			if err := proto.Unmarshal(body, req); err != nil {
				http.Error(w, "invalid protobuf body: "+err.Error(), http.StatusBadRequest)
				return
			}
		case "application/json":
			if err := protojson.Unmarshal(body, req); err != nil {
				http.Error(w, "invalid json body: "+err.Error(), http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "unsupported content-type: "+ct, http.StatusUnsupportedMediaType)
			return
		}

		// Reuse the gRPC handler — same proto, same aggregator, same
		// dedup, same incident lifecycle. The only difference between
		// transports is wire format.
		resp, err := recv.Export(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// OTLP/HTTP responses must mirror the request encoding when
		// possible. The body is empty on success either way, but
		// honoring Content-Type makes the response valid for strict
		// clients that parse it (the spec mandates a partial-success
		// envelope on errors, which we do not yet emit since we never
		// reject records — see the comment in LogsReceiver.Export).
		switch ct {
		case "application/json":
			payload, marshalErr := protojson.Marshal(resp)
			if marshalErr != nil {
				http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		default:
			payload, marshalErr := proto.Marshal(resp)
			if marshalErr != nil {
				http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		}
	})
	return mux
}

// ServeHTTP starts an OTLP/HTTP server that accepts log requests and
// delegates to recv.Export. It mirrors Serve (the gRPC variant) so the
// exporter binary can run both transports side by side: gRPC on :4317 and
// HTTP on :4318, the default OTLP ports per the spec.
func ServeHTTP(ctx context.Context, addr string, recv *LogsReceiver, log logr.Logger) (*http.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}

	srv := &http.Server{
		Handler:           NewHTTPHandler(recv),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("OTLP logs HTTP receiver listening", "address", addr, "path", otlpHTTPLogsPath)

	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(err, "OTLP logs HTTP server stopped")
		}
	}()

	go func() {
		<-ctx.Done()
		log.Info("Shutting down OTLP logs HTTP receiver")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	return srv, lis, nil
}

// readRequestBody enforces the size cap and decompresses gzip-encoded
// requests. Decompressing inside the handler (rather than relying on a
// reverse proxy) lets the exporter run as a flat Deployment without
// requiring an Ingress to do gzip translation.
func readRequestBody(r *http.Request) ([]byte, error) {
	var src io.Reader = http.MaxBytesReader(nil, r.Body, httpMaxRequestBytes)

	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		src = gz
	}

	body, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// contentType strips any "; charset=..." suffix from the Content-Type
// header so the switch above sees the bare media type. The OTLP spec only
// defines two media types and neither carries parameters, but real-world
// clients (curl, browsers, the OTel Collector) sometimes append charset
// anyway and we should not 415 them.
func contentType(h string) string {
	if i := strings.Index(h, ";"); i >= 0 {
		return strings.TrimSpace(h[:i])
	}
	return strings.TrimSpace(h)
}
