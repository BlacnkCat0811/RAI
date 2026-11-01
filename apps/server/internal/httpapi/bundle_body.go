package httpapi

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"time"

	"rai/server/internal/syncing"
)

const (
	MaxBundleFramingBytes    int64 = 64 << 10
	MaxBundleWireBytes       int64 = syncing.MaxWorkoutBundleBytes + MaxBundleFramingBytes
	MaxBundleCompressedBytes int64 = 8 << 20
)

type countedBody struct {
	io.ReadCloser
	bytes int64
}

func (r *countedBody) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	r.bytes += int64(n)
	return n, e
}

type bundleReadMetrics struct {
	WireBytes     int64
	FileBytes     int64
	DecodedBytes  int
	Multipart     time.Duration
	Decompression time.Duration
}

func bundleReadError(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return syncing.Fail(413, "wire_body_too_large")
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return syncing.Fail(408, "upload_read_timeout")
	}
	return syncing.Fail(400, "body_read_failed")
}

// Limit the original HTTP stream before a multipart reader can buffer or drain
// it. Do not Close rejected parts: Part.Close drains its remaining content.
func readBundleBody(w http.ResponseWriter, r *http.Request) (raw []byte, m bundleReadMetrics, err error) {
	start := time.Now()
	defer func() {
		m.Multipart = time.Since(start) - m.Decompression
		if err != nil {
			r.Close = true
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		}
	}()
	if r.ContentLength > MaxBundleWireBytes {
		return nil, m, syncing.Fail(413, "wire_body_too_large")
	}
	media, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "multipart/form-data" {
		return nil, m, syncing.Fail(415, "unsupported_media_type")
	}
	if len(params["boundary"]) == 0 || len(params["boundary"]) > 70 {
		return nil, m, syncing.Fail(422, "unsupported_payload")
	}
	if r.Header.Get("Content-Encoding") != "" {
		return nil, m, syncing.Fail(415, "unsupported_content_encoding")
	}
	body := &countedBody{ReadCloser: http.MaxBytesReader(w, r.Body, MaxBundleWireBytes)}
	r.Body = body
	defer func() { m.WireBytes = body.bytes }()
	reader, e := r.MultipartReader()
	if e != nil {
		return nil, m, syncing.Fail(422, "unsupported_payload")
	}
	part, e := reader.NextRawPart()
	if e != nil {
		return nil, m, bundleReadError(e)
	}
	disposition, fields, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if e != nil || disposition != "form-data" || fields["name"] != "file" || fields["filename"] == "" || len(fields) != 2 {
		return nil, m, syncing.Fail(422, "unsupported_payload")
	}
	for key, values := range part.Header {
		if len(values) != 1 {
			return nil, m, syncing.Fail(422, "unsupported_payload")
		}
		if key != "Content-Disposition" && key != "Content-Type" {
			return nil, m, syncing.Fail(422, "unsupported_payload")
		}
	}
	media, _, e = mime.ParseMediaType(part.Header.Get("Content-Type"))
	if e != nil || (media != "application/json" && media != "application/octet-stream" && media != "application/gzip") {
		return nil, m, syncing.Fail(415, "unsupported_media_type")
	}

	if media == "application/gzip" {
		zstart := time.Now()
		compressed := &countedReader{reader: io.LimitReader(part, MaxBundleCompressedBytes+1)}
		buffered := bufio.NewReader(compressed)
		gz, gerr := gzip.NewReader(buffered)
		if gerr != nil {
			return nil, m, gzipReadError(gerr, compressed.bytes)
		}
		gz.Multistream(false)
		var decoded bytes.Buffer
		buffer := make([]byte, 32<<10)
		for {
			if r.Context().Err() != nil {
				_ = gz.Close()
				return nil, m, r.Context().Err()
			}
			if time.Since(zstart)-compressed.readTime > 5*time.Second {
				_ = gz.Close()
				return nil, m, syncing.Fail(503, "decompression_timeout")
			}
			n, readErr := gz.Read(buffer)
			if decoded.Len()+n > syncing.MaxWorkoutBundleBytes {
				_ = gz.Close()
				return nil, m, syncing.Fail(413, "bundle_too_large")
			}
			if int64(decoded.Len()+n) > 100*compressed.bytes+(64<<10) {
				_ = gz.Close()
				return nil, m, syncing.Fail(413, "compression_ratio_exceeded")
			}
			_, _ = decoded.Write(buffer[:n])
			if compressed.bytes > MaxBundleCompressedBytes {
				_ = gz.Close()
				return nil, m, syncing.Fail(413, "compressed_file_too_large")
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = gz.Close()
				return nil, m, gzipReadError(readErr, compressed.bytes)
			}
		}
		_ = gz.Close()
		// Reject concatenated members and trailing garbage. CRC/size were checked
		// by Read reaching EOF; a successful Close alone does not validate them.
		if _, tailErr := buffered.ReadByte(); tailErr != io.EOF {
			if tailErr != nil {
				return nil, m, gzipReadError(tailErr, compressed.bytes)
			}
			return nil, m, syncing.Fail(422, "invalid_gzip")
		}
		raw = decoded.Bytes()
		m.FileBytes = compressed.bytes
		m.Decompression = time.Since(zstart) - compressed.readTime
	} else {
		raw, e = io.ReadAll(io.LimitReader(part, syncing.MaxWorkoutBundleBytes+1))
		m.FileBytes = int64(len(raw))
		if e != nil {
			return nil, m, bundleReadError(e)
		}
	}
	m.DecodedBytes = len(raw)
	if len(raw) > syncing.MaxWorkoutBundleBytes {
		return nil, m, syncing.Fail(413, "bundle_too_large")
	}
	if len(raw) == 0 {
		return nil, m, syncing.Fail(422, "unsupported_payload")
	}

	// Read just the next part's headers, never its content. Exactly one part.
	if _, e = reader.NextRawPart(); e != io.EOF {
		if e != nil {
			return nil, m, bundleReadError(e)
		}
		return nil, m, syncing.Fail(422, "unsupported_payload")
	}
	// multipart stops at its closing boundary. Count any epilogue against the
	// wire/framing budget too; MaxBytesReader prevents an unlimited tail.
	if _, e = io.Copy(io.Discard, body); e != nil {
		return nil, m, bundleReadError(e)
	}
	if body.bytes-m.FileBytes > MaxBundleFramingBytes {
		return nil, m, syncing.Fail(413, "multipart_framing_too_large")
	}
	return raw, m, nil
}

type countedReader struct {
	reader   io.Reader
	bytes    int64
	readTime time.Duration
}

func (r *countedReader) Read(p []byte) (int, error) {
	start := time.Now()
	n, e := r.reader.Read(p)
	r.readTime += time.Since(start)
	r.bytes += int64(n)
	return n, e
}
func gzipReadError(err error, n int64) error {
	if n > MaxBundleCompressedBytes {
		return syncing.Fail(413, "compressed_file_too_large")
	}
	var limit *http.MaxBytesError
	var network net.Error
	if errors.As(err, &limit) || (errors.As(err, &network) && network.Timeout()) {
		return bundleReadError(err)
	}
	return syncing.Fail(422, "invalid_gzip")
}
