package helps

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestKiroProxyChainFallsBackAfterTransportFailure(t *testing.T) {
	var successfulProxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successfulProxyCalls.Add(1)
		if r.URL.String() != "http://upstream.example/generate" {
			t.Fatalf("proxy request URL = %q", r.URL)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	kiroProxyChainMu.Lock()
	kiroProxyCooling = make(map[string]time.Time)
	kiroProxyChainMu.Unlock()

	client := NewKiroProxyChainHTTPClient(0, []string{"http://127.0.0.1:1", proxy.URL})
	req, err := http.NewRequest(http.MethodPost, "http://upstream.example/generate", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy chain request: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if successfulProxyCalls.Load() != 1 {
		t.Fatalf("successful proxy calls = %d, want 1", successfulProxyCalls.Load())
	}

	kiroProxyChainMu.Lock()
	_, cooling := kiroProxyCooling["http://127.0.0.1:1"]
	kiroProxyChainMu.Unlock()
	if !cooling {
		t.Fatal("transport-failing proxy was not cooled")
	}
}
