//go:build windows

package clipboard

import "testing"

func TestDetectWindows(t *testing.T) {
	for _, backend := range []string{"auto", "windows"} {
		t.Run(backend, func(t *testing.T) {
			// Detection only constructs the adapter; do not read or write the clipboard.
			b, err := Detect(backend, Env{GOOS: "windows"}, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if b.Adapter == nil || b.Adapter.Name() != "windows" {
				t.Fatalf("expected windows adapter, got %+v", b)
			}
			if b.Watcher != nil || b.PollFallback != b.Adapter {
				t.Fatal("windows must poll the same adapter without a watcher")
			}
		})
	}
}
