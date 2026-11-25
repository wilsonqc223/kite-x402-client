package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadKeyRefusesAWorldReadableKeyFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply")
	}
	dir := t.TempDir()
	// Stand-in file content: LoadKey only trims and returns it, and it must
	// not look like key material.
	secret := "key-file contents for the permission test"

	lax := filepath.Join(dir, "lax.key")
	if err := os.WriteFile(lax, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(lax); err == nil {
		t.Fatal("a 0644 key file must be refused")
	}

	strict := filepath.Join(dir, "strict.key")
	if err := os.WriteFile(strict, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadKey(strict)
	if err != nil {
		t.Fatalf("a 0600 key file must load: %v", err)
	}
	if got != secret {
		t.Errorf("key mismatch: loaded %d chars, want %d", len(got), len(secret))
	}
}

func TestLoadKeyFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("KITE_PRIVATE_KEY", "  0xabc  \n")
	got, err := LoadKey("")
	if err != nil || got != "0xabc" {
		t.Errorf("LoadKey from env = %q, %v; want trimmed 0xabc", got, err)
	}
	t.Setenv("KITE_PRIVATE_KEY", "")
	if _, err := LoadKey(""); err == nil {
		t.Error("no key anywhere must be an error")
	}
}

func TestHTTPClientForKeepsTheDefaultTransportSettings(t *testing.T) {
	client, err := HTTPClientFor("socks5://127.0.0.1:10800", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("want an *http.Transport proxying client, got %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Error("proxy function not installed")
	}
	defaults := http.DefaultTransport.(*http.Transport)
	// The default transport proxies from the environment (HTTP(S)_PROXY);
	// it must be untouched after we install an explicit proxy on the clone.
	defaultsProxyBefore := fmt.Sprintf("%p", defaults.Proxy)
	// Cloning the default transport must carry over its tuning; a bare
	// &http.Transport{} would leave these at zero.
	if transport.TLSHandshakeTimeout != defaults.TLSHandshakeTimeout || transport.TLSHandshakeTimeout == 0 {
		t.Errorf("TLSHandshakeTimeout = %v, want the default %v", transport.TLSHandshakeTimeout, defaults.TLSHandshakeTimeout)
	}
	if transport.IdleConnTimeout != defaults.IdleConnTimeout || transport.IdleConnTimeout == 0 {
		t.Errorf("IdleConnTimeout = %v, want the default %v", transport.IdleConnTimeout, defaults.IdleConnTimeout)
	}
	if transport.ForceAttemptHTTP2 != defaults.ForceAttemptHTTP2 {
		t.Errorf("ForceAttemptHTTP2 = %v, want the default %v", transport.ForceAttemptHTTP2, defaults.ForceAttemptHTTP2)
	}
	if got := fmt.Sprintf("%p", defaults.Proxy); got != defaultsProxyBefore {
		t.Error("installing a proxy mutated http.DefaultTransport")
	}
}

func TestHTTPClientForRejectsSchemelessProxyURLs(t *testing.T) {
	if _, err := HTTPClientFor("127.0.0.1:10800", time.Second); err == nil {
		t.Error("a proxy URL without a scheme must be rejected, not silently treated as direct")
	}
	if _, err := HTTPClientFor("://bad", time.Second); err == nil {
		t.Error("an unparseable proxy URL must be rejected")
	}
	if _, err := HTTPClientFor("", time.Second); err != nil {
		t.Errorf("no proxy must be fine: %v", err)
	}
}
