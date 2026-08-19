package requestbody

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidUTF8Body     = errors.New("invalid utf-8 request body")
	ErrInvalidJSONBody     = errors.New("invalid json request body")
	errRequestBodyTooLarge = errors.New("request body too large")
)

const maxJSONUTF8ValidationSize = 100 << 20

// Administrative JSON is bounded before any handler decoder runs. Individual
// routes may apply a smaller limit (for example the 8 KiB login body or the
// 16 MiB batch-account body), but none should trigger the public 100 MiB copy.
const maxAdminJSONUTF8ValidationSize = 16 << 20
const maxAdminLoginJSONUTF8ValidationSize = 8 << 10

// ValidateJSONUTF8 validates complete JSON request bodies before downstream
// decoders can silently replace malformed UTF-8 or stop before trailing bytes.
func ValidateJSONUTF8(next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldValidateJSONBody(r) {
			r.Body = validateAndReplayBody(r.Body, jsonValidationLimit(r), true)
		}
		next.ServeHTTP(w, r)
	})
}

func shouldValidateJSONBody(r *http.Request) bool {
	if r == nil || r.Body == nil {
		return false
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	return isJSONContentType(r.Header.Get("Content-Type")) || isKnownJSONRequestPath(r.Method, path)
}

func isJSONContentType(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mediaType = raw
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return strings.Contains(mediaType, "json")
}

func isKnownJSONRequestPath(method, path string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	switch {
	case path == "/v1/chat/completions" || path == "/chat/completions":
		return true
	case path == "/v1/responses" || path == "/responses":
		return true
	case path == "/v1/embeddings" || path == "/embeddings":
		return true
	case path == "/anthropic/v1/messages" || path == "/v1/messages" || path == "/messages":
		return true
	case path == "/anthropic/v1/messages/count_tokens" || path == "/v1/messages/count_tokens" || path == "/messages/count_tokens":
		return true
	case strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/"):
		return strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent")
	case strings.HasPrefix(path, "/admin/"):
		return true
	default:
		return false
	}
}

func jsonValidationLimit(r *http.Request) int64 {
	if r != nil && r.URL != nil && strings.TrimSpace(r.URL.Path) == "/admin/login" {
		return maxAdminLoginJSONUTF8ValidationSize
	}
	if r != nil && r.URL != nil && strings.HasPrefix(strings.TrimSpace(r.URL.Path), "/admin/") {
		return maxAdminJSONUTF8ValidationSize
	}
	return maxJSONUTF8ValidationSize
}

func validateAndReplayBody(body io.ReadCloser, limit int64, validateJSON bool) io.ReadCloser {
	if body == nil {
		return body
	}
	if limit <= 0 {
		limit = maxJSONUTF8ValidationSize
	}
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return &errorReadCloser{err: err, closer: body}
	}
	if int64(len(raw)) > limit {
		return &errorReadCloser{err: errRequestBodyTooLarge, closer: body}
	}
	if !utf8.Valid(raw) {
		return &errorReadCloser{err: ErrInvalidUTF8Body, closer: body}
	}
	if validateJSON && len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		var value any
		if err := decoder.Decode(&value); err != nil {
			return &errorReadCloser{err: ErrInvalidJSONBody, closer: body}
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return &errorReadCloser{err: ErrInvalidJSONBody, closer: body}
		}
	}
	return &replayReadCloser{Reader: bytes.NewReader(raw), closer: body}
}

type replayReadCloser struct {
	*bytes.Reader
	closer io.Closer
}

func (r *replayReadCloser) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

type errorReadCloser struct {
	err    error
	closer io.Closer
}

func (r *errorReadCloser) Read([]byte) (int, error) {
	if r == nil || r.err == nil {
		return 0, io.EOF
	}
	return 0, r.err
}

func (r *errorReadCloser) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer.Close()
}
