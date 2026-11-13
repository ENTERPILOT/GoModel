package usagemetrics

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValueLimitAdmitsExactlyTheLimitUnderConcurrency(t *testing.T) {
	const limit, values, workers = 10, 50, 16
	var l valueLimit
	var mu sync.Mutex
	admitted := map[string]struct{}{}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range values {
				value := "value-" + strconv.Itoa(i)
				got := l.admit(value, OtherValue, limit)
				if got == OtherValue {
					continue
				}
				assert.Equal(t, value, got)
				mu.Lock()
				admitted[got] = struct{}{}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	assert.Len(t, admitted, limit)
	assert.Empty(t, l.admit("", OtherValue, limit), "the empty value is never counted or replaced")
}
