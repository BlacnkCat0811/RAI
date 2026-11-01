package httpapi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"rai/server/internal/syncing"
	"strings"
	"testing"
)

type endlessBytes struct{ remaining, read int64 }

func (b *endlessBytes) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.remaining)
	for i := range p[:n] {
		p[i] = ' '
	}
	b.remaining -= n
	b.read += n
	return int(n), nil
}
func bodyCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *syncing.Error
	if !errors.As(err, &e) || e.Status != status || (code != "" && e.Code != code) {
		t.Fatalf("error=%v want %d %s", err, status, code)
	}
}
func plainMultipart(raw []byte) ([]byte, string) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, _ := w.CreatePart(mapHeader("application/json"))
	_, _ = p.Write(raw)
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

func mapHeader(media string) textproto.MIMEHeader {
	return textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="workout.json"`}, "Content-Type": {media}}
}
func TestBundleWireLimitsAndStrictMultipart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{"unknown", "--x\r\nContent-Disposition: form-data; name=other; filename=x\r\nContent-Type: application/json\r\n\r\n{}\r\n--x--\r\n", 422},
		{"duplicate", "--x\r\nContent-Disposition: form-data; name=file; filename=x\r\nContent-Type: application/json\r\n\r\n{}\r\n--x\r\nContent-Disposition: form-data; name=file; filename=x\r\n\r\nignored\r\n--x--\r\n", 422},
		{"empty", "--x\r\nContent-Disposition: form-data; name=file; filename=x\r\nContent-Type: application/json\r\n\r\n\r\n--x--\r\n", 422},
		{"malformed", "--x\r\nnot-a-header\r\n", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "multipart/form-data; boundary=x")
			_, _, err := readBundleBody(httptest.NewRecorder(), r)
			bodyCode(t, err, tc.status, "")
		})
	}
	t.Run("length-fast-reject", func(t *testing.T) {
		b := &countingBody{}
		r := httptest.NewRequest("POST", "/", b)
		r.ContentLength = MaxBundleWireBytes + 1
		r.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		_, _, err := readBundleBody(httptest.NewRecorder(), r)
		bodyCode(t, err, 413, "wire_body_too_large")
		if b.reads != 0 {
			t.Fatal("oversize length read")
		}
	})
	for _, chunked := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-content-length", true: "chunked"}[chunked], func(t *testing.T) {
			raw, media := plainMultipart([]byte("{}"))
			tail := &endlessBytes{remaining: 1 << 30}
			r := httptest.NewRequest("POST", "/", io.MultiReader(bytes.NewReader(raw), tail))
			r.ContentLength = -1
			if chunked {
				r.TransferEncoding = []string{"chunked"}
			}
			r.Header.Set("Content-Type", media)
			_, m, err := readBundleBody(httptest.NewRecorder(), r)
			bodyCode(t, err, 413, "wire_body_too_large")
			if m.WireBytes > MaxBundleWireBytes+1 || tail.read > MaxBundleWireBytes+1 {
				t.Fatal("wire overread", m.WireBytes, tail.read)
			}
		})
	}
	t.Run("extra-part-does-not-drain", func(t *testing.T) {
		prefix := "--x\r\nContent-Disposition: form-data; name=file; filename=x\r\nContent-Type: application/json\r\n\r\n{}\r\n--x\r\nContent-Disposition: form-data; name=extra\r\n\r\n"
		tail := &endlessBytes{remaining: 1 << 30}
		r := httptest.NewRequest("POST", "/", io.MultiReader(strings.NewReader(prefix), tail))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		_, _, err := readBundleBody(httptest.NewRecorder(), r)
		bodyCode(t, err, 422, "unsupported_payload")
		if tail.read > 4096 {
			t.Fatal("extra part drained", tail.read)
		}
	})
	t.Run("plain", func(t *testing.T) {
		body, media := plainMultipart([]byte("{}"))
		r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		r.Header.Set("Content-Type", media)
		raw, m, err := readBundleBody(httptest.NewRecorder(), r)
		if err != nil || string(raw) != "{}" || m.WireBytes != int64(len(body)) {
			t.Fatal("plain", err, m)
		}
	})
}

func TestBundleGzipBoundaries(t *testing.T) {
	compress := func(raw []byte) []byte {
		var b bytes.Buffer
		g := gzip.NewWriter(&b)
		_, _ = g.Write(raw)
		_ = g.Close()
		return b.Bytes()
	}
	valid := compress([]byte("{}"))
	crc := append([]byte(nil), valid...)
	crc[len(crc)-8] ^= 255
	for _, tc := range []struct {
		name   string
		data   []byte
		status int
		code   string
	}{
		{"valid", valid, 0, ""}, {"truncated", valid[:len(valid)-4], 422, "invalid_gzip"}, {"crc", crc, 422, "invalid_gzip"}, {"garbage", []byte("bad gzip"), 422, "invalid_gzip"}, {"concatenated", append(append([]byte(nil), valid...), valid...), 422, "invalid_gzip"}, {"bomb", compress(bytes.Repeat([]byte("A"), 100<<20)), 413, "compression_ratio_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			w := multipart.NewWriter(&b)
			p, _ := w.CreatePart(mapHeader("application/gzip"))
			_, _ = p.Write(tc.data)
			_ = w.Close()
			r := httptest.NewRequest("POST", "/", &b)
			r.Header.Set("Content-Type", w.FormDataContentType())
			raw, _, err := readBundleBody(httptest.NewRecorder(), r)
			if tc.status == 0 {
				if err != nil || string(raw) != "{}" {
					t.Fatal("gzip", err)
				}
			} else {
				bodyCode(t, err, tc.status, tc.code)
			}
		})
	}
	t.Run("decoded-limit-before-allocation", func(t *testing.T) {
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		rng := rand.New(rand.NewSource(42))
		block := make([]byte, 32768)
		for i := 0; i < 1200; i++ {
			_, _ = rng.Read(block[:4096])
			_, _ = gz.Write(block)
		}
		_ = gz.Close()
		if int64(compressed.Len()) >= MaxBundleCompressedBytes {
			t.Fatal("fixture wire")
		}
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		p, _ := w.CreatePart(mapHeader("application/gzip"))
		_, _ = p.Write(compressed.Bytes())
		_ = w.Close()
		r := httptest.NewRequest("POST", "/", &b)
		r.Header.Set("Content-Type", w.FormDataContentType())
		_, _, err := readBundleBody(httptest.NewRecorder(), r)
		bodyCode(t, err, 413, "bundle_too_large")
	})
}

type timeoutSQLState string

func (e timeoutSQLState) Error() string    { return "redacted synthetic database fault" }
func (e timeoutSQLState) SQLState() string { return string(e) }
func TestDatabaseTimeoutClassificationIsRetryable(t *testing.T) {
	api := &API{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for state, code := range map[string]string{"57014": "database_statement_timeout_or_cancel", "25P04": "database_transaction_timeout"} {
		r := httptest.NewRecorder()
		api.fail(r, timeoutSQLState(state))
		if r.Code != 503 || !strings.Contains(r.Body.String(), code) || r.Header().Get("Retry-After") == "" {
			t.Fatal("timeout became permanent", state, r.Code)
		}
	}
}

func TestBundleCompressedFileIndependentLimit(t *testing.T) {
	raw := make([]byte, 9<<20)
	_, _ = rand.New(rand.NewSource(76)).Read(raw)
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	_, _ = gz.Write(raw)
	_ = gz.Close()
	if int64(zipped.Len()) <= MaxBundleCompressedBytes {
		t.Fatal("fixture did not exceed compressed limit")
	}
	var wire bytes.Buffer
	mw := multipart.NewWriter(&wire)
	p, _ := mw.CreatePart(mapHeader("application/gzip"))
	_, _ = p.Write(zipped.Bytes())
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/", &wire)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	_, _, err := readBundleBody(httptest.NewRecorder(), r)
	bodyCode(t, err, 413, "compressed_file_too_large")
}
