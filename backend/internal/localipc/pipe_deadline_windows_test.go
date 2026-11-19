//go:build windows

package localipc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// net/http asserts the background-read error directly to net.Error (no
// errors.As). A deadline wrapped in *fs.PathError fails that assertion and the
// server cancels the in-flight request context, so the pipe conn must return
// the timeout itself.
func TestWindowsPipeConnDeadlineIsNetErrorTimeout(t *testing.T) {
	endpoint := AgentHostEndpoint()
	ln, err := Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialContext(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()

	if err := server.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	_, readErr := server.Read(make([]byte, 1))
	netErr, ok := readErr.(net.Error)
	if !ok || !netErr.Timeout() {
		t.Fatalf("deadline read error = %v (%T), want a net.Error timeout", readErr, readErr)
	}
}

// End-to-end: requests served over the named pipe must keep their context
// alive while the handler works, for bodyless GETs and for POSTs.
func TestHTTPOverWindowsPipeKeepsRequestContext(t *testing.T) {
	endpoint := AgentHostEndpoint()
	ln, err := Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(30 * time.Millisecond)
		if ctxErr := r.Context().Err(); ctxErr != nil {
			http.Error(w, ctxErr.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return DialContext(ctx, endpoint)
			},
			DisableKeepAlives: true,
		},
		Timeout: 10 * time.Second,
	}
	for i := 0; i < 10; i++ {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, reqErr := http.NewRequest(method, "http://beeftv.local/x", nil)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			resp, doErr := client.Do(req)
			if doErr != nil {
				t.Fatalf("%s #%d: %v", method, i, doErr)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s #%d: status %d body %q", method, i, resp.StatusCode, fmt.Sprint(string(body)))
			}
		}
	}
}
