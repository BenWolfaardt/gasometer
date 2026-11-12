// Package gasometertest holds the contract suites every adapter of a port runs, and the concurrent-callers driver they
// share, in the manner of net/http/httptest. Each suite takes a constructor, so the in-memory fakes and the real
// adapters are held to the same contract.
package gasometertest

import (
	"sync"
	"testing"
)

// Concurrently runs f(0) through f(n-1) at once and returns when every call has. f reports failures with t.Errorf:
// t.Fatal must not be called from these goroutines.
func Concurrently(t *testing.T, n int, f func(i int)) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			f(i)
		})
	}
	close(start)
	wg.Wait()
}
