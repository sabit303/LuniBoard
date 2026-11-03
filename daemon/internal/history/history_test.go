package history

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func entry(i int) Entry {
	return Entry{EventID: fmt.Sprintf("e%d", i), Direction: Outgoing, Text: fmt.Sprintf("text %d", i), Timestamp: time.Unix(int64(i), 0)}
}

func TestBoundedNewestFirst(t *testing.T) {
	s, _ := Open(Options{Enabled: true, MaxEntries: 3})
	for i := 0; i < 5; i++ {
		s.Add(entry(i))
	}
	l := s.List(0)
	if len(l) != 3 || l[0].EventID != "e4" || l[2].EventID != "e2" {
		t.Fatalf("unexpected %+v", l)
	}
	if len(s.List(1)) != 1 {
		t.Fatal("limit ignored")
	}
}

func TestDisabledRecordsNothing(t *testing.T) {
	s, _ := Open(Options{Enabled: false, MaxEntries: 3})
	s.Add(entry(1))
	if len(s.List(0)) != 0 {
		t.Fatal("disabled history stored an entry")
	}
}

func TestDeliveries(t *testing.T) {
	s, _ := Open(Options{Enabled: true, MaxEntries: 3})
	s.Add(entry(1))
	s.UpdateDelivery("e1", "phone", DeliverySent)
	s.UpdateDelivery("e1", "phone", "applied")
	if got := s.List(0)[0].Deliveries["phone"]; got != "applied" {
		t.Fatalf("delivery = %q", got)
	}
}

func TestPersistenceAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, _ := Open(Options{Enabled: true, Persist: true, MaxEntries: 10, Path: path})
	s.Add(entry(1))
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	s2, _ := Open(Options{Enabled: true, Persist: true, MaxEntries: 10, Path: path})
	if len(s2.List(0)) != 1 {
		t.Fatal("entries not loaded")
	}
	if err := s2.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clear did not remove file")
	}
}

func TestDisablingPersistenceErasesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, _ := Open(Options{Enabled: true, Persist: true, MaxEntries: 10, Path: path})
	s.Add(entry(1))
	if _, err := Open(Options{Enabled: true, Persist: false, MaxEntries: 10, Path: path}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("persisted history should be erased when persistence is disabled")
	}
}
