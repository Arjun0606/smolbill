package memory

import (
	"sync"
	"testing"
)

func TestNextInvoiceNumber(t *testing.T) {
	s := New()
	for i, want := range []string{"INV-000001", "INV-000002", "INV-000003"} {
		got, err := s.NextInvoiceNumber()
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("step %d: got %q, want %q", i, got, want)
		}
	}
}

// Numbers must stay unique under concurrent finalize.
func TestNextInvoiceNumberConcurrent(t *testing.T) {
	s := New()
	const n = 200
	seen := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			num, _ := s.NextInvoiceNumber()
			seen <- num
		}()
	}
	wg.Wait()
	close(seen)
	uniq := map[string]bool{}
	for num := range seen {
		if uniq[num] {
			t.Fatalf("duplicate invoice number %q", num)
		}
		uniq[num] = true
	}
	if len(uniq) != n {
		t.Fatalf("got %d unique numbers, want %d", len(uniq), n)
	}
}
