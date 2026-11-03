package device

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

func testDevice(id, name string) Device {
	return Device{
		ID:          id,
		Name:        name,
		Platform:    protocol.PlatformAndroid,
		Fingerprint: protocol.CertificateFingerprint([]byte(id)),
		PairedAt:    time.Unix(1700000000, 0).UTC(),
		Permissions: Permissions{Send: true, Receive: true},
	}
}

func TestRegistryPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var changes []Change
	r.Subscribe(func(c Change) { changes = append(changes, c) })

	if err := r.Add(testDevice("phone", "Pixel")); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(testDevice("laptop", "Arch")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetPermissions("phone", Permissions{Send: false, Receive: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordSeen("laptop", "192.168.1.5:47823", time.Now()); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("devices file mode %o", st.Mode().Perm())
	}

	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list := r2.List()
	if len(list) != 2 || list[0].Name != "Arch" || list[1].Name != "Pixel" {
		t.Fatalf("unexpected list %+v", list)
	}
	if list[1].Permissions.Send {
		t.Fatal("permissions not persisted")
	}
	if len(list[0].Addresses) != 1 || list[0].Addresses[0] != "192.168.1.5:47823" {
		t.Fatalf("addresses not persisted: %v", list[0].Addresses)
	}
	if d, ok := r2.ByFingerprint(protocol.CertificateFingerprint([]byte("phone"))); !ok || d.ID != "phone" {
		t.Fatal("ByFingerprint failed")
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 notifications (2 add + 1 update), got %d", len(changes))
	}
}

func TestRegistryRemove(t *testing.T) {
	r, _ := Open("")
	_ = r.Add(testDevice("phone", "Pixel"))
	var removed string
	r.Subscribe(func(c Change) {
		if c.Kind == Removed {
			removed = c.Device.ID
		}
	})
	if _, err := r.Remove("phone"); err != nil {
		t.Fatal(err)
	}
	if removed != "phone" {
		t.Fatal("removal not notified")
	}
	if _, ok := r.Get("phone"); ok {
		t.Fatal("device still present")
	}
	if _, err := r.Remove("phone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegistryAddReplacesSameFingerprint(t *testing.T) {
	r, _ := Open("")
	a := testDevice("old-id", "Pixel")
	_ = r.Add(a)
	b := testDevice("new-id", "Pixel")
	b.Fingerprint = a.Fingerprint
	if err := r.Add(b); err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 1 {
		t.Fatal("expected re-pair with same certificate to replace the old entry")
	}
}

func TestRegistryRejectsInvalid(t *testing.T) {
	r, _ := Open("")
	d := testDevice("phone", "Pixel")
	d.Fingerprint = "nope"
	if err := r.Add(d); err == nil {
		t.Fatal("expected invalid fingerprint error")
	}
	d = testDevice("bad id", "Pixel")
	if err := r.Add(d); err == nil {
		t.Fatal("expected invalid id error")
	}
}
