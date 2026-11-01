package httpapi

import (
	"net/http"
	"time"
)

const (
	ReadHeaderBudget       = 5 * time.Second
	BundleBodyBudget       = 180 * time.Second
	BundleProcessingBudget = 60 * time.Second
	BundleHandlerBudget    = 245 * time.Second
	ACKWriteBudget         = 5 * time.Second
)

// Shared by the executable and real-socket transport tests. Read/WriteTimeout
// are finite fallback deadlines; authenticated routes set their own phase
// deadlines with ResponseController (including HTTP/2 stream deadlines).
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: ReadHeaderBudget,
		ReadTimeout: ReadHeaderBudget + BundleBodyBudget, WriteTimeout: ReadHeaderBudget + BundleHandlerBudget + ACKWriteBudget,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
}
