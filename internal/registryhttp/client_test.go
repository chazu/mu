package registryhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStalledHeadersAndBodiesAreBounded(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[body], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body {
					w.WriteHeader(200)
					io.WriteString(w, "prefix")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := NewClient(Options{HeaderTimeout: 60 * time.Millisecond, IdleTimeout: 80 * time.Millisecond})
			start := time.Now()
			response, err := client.Get(server.URL)
			if response != nil {
				_, err = io.ReadAll(response.Body)
				response.Body.Close()
			}
			if err == nil {
				t.Fatal("stalled I/O succeeded")
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("unbounded stalled I/O: %s", elapsed)
			}
		})
	}
}

func TestProgressingTransferCanOutliveHeaderAndIdleBudgets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := 0; i < 12; i++ {
			time.Sleep(20 * time.Millisecond)
			io.WriteString(w, strings.Repeat("x", 1000))
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	client := NewClient(Options{HeaderTimeout: 50 * time.Millisecond, IdleTimeout: 100 * time.Millisecond})
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || len(data) != 12000 {
		t.Fatalf("cut off progressing transfer: %d, %v", len(data), err)
	}
}

func TestOfflineRequestNeverContactsServer(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	request, _ := http.NewRequestWithContext(WithOffline(context.Background()), "GET", server.URL, nil)
	_, err := NewClient(Options{}).Do(request)
	if !errors.Is(err, ErrOffline) || requests.Load() != 0 {
		t.Fatalf("offline request: %v, %d calls", err, requests.Load())
	}
}
