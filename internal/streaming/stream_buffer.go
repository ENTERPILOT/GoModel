package streaming

import "sync"

const (
	defaultStreamBufferCapacity = 1024
	maxPooledStreamBufferSize   = 64 * 1024
)

var streamBufferPool = sync.Pool{
	New: func() any {
		buffer := make([]byte, 0, defaultStreamBufferCapacity)
		return &buffer
	},
}

// StreamBuffer is a non-concurrent FIFO byte buffer for short-lived stream converters.
// It must not be copied after first use.
type StreamBuffer struct {
	data   []byte
	pooled *[]byte
	read   int
}

func NewStreamBuffer(initialCapacity int) StreamBuffer {
	if initialCapacity < defaultStreamBufferCapacity {
		initialCapacity = defaultStreamBufferCapacity
	}

	pooled := streamBufferPool.Get().(*[]byte)
	data := (*pooled)[:0]
	if cap(data) == 0 || cap(data) > maxPooledStreamBufferSize || cap(data) < initialCapacity {
		data = make([]byte, 0, initialCapacity)
		*pooled = data
	}

	return StreamBuffer{
		data:   data[:0],
		pooled: pooled,
	}
}

func (b *StreamBuffer) Len() int {
	if b == nil || b.read >= len(b.data) {
		return 0
	}
	return len(b.data) - b.read
}

func (b *StreamBuffer) Unread() []byte {
	if b.Len() == 0 {
		return nil
	}
	return b.data[b.read:]
}

func (b *StreamBuffer) AppendBytes(data []byte) {
	if len(data) == 0 {
		return
	}
	b.prepareAppend()
	b.data = append(b.data, data...)
}

func (b *StreamBuffer) AppendString(data string) {
	if data == "" {
		return
	}
	b.prepareAppend()
	b.data = append(b.data, data...)
}

func (b *StreamBuffer) Read(p []byte) int {
	if len(p) == 0 || b.Len() == 0 {
		return 0
	}

	n := copy(p, b.data[b.read:])
	b.Consume(n)
	return n
}

func (b *StreamBuffer) Consume(n int) {
	if n <= 0 || b.Len() == 0 {
		return
	}
	if n >= b.Len() {
		b.data = b.data[:0]
		b.read = 0
		return
	}
	b.read += n
}

// Release returns the buffer's storage to the pool. No slice derived from the
// buffer (Unread, or bytes handed to a decoder that may alias its input) may
// be retained past this call: the storage is immediately reusable by another
// stream and retained views would see another request's data.
func (b *StreamBuffer) Release() {
	if b == nil {
		return
	}

	if b.pooled != nil {
		// Prefer returning the live data buffer: appends may have grown it past
		// the originally pooled allocation, and recycling the grown buffer is
		// what lets later streams skip re-growing from scratch.
		pooledData := b.data
		if cap(pooledData) == 0 || cap(pooledData) > maxPooledStreamBufferSize {
			pooledData = *b.pooled
			if cap(pooledData) == 0 || cap(pooledData) > maxPooledStreamBufferSize {
				pooledData = make([]byte, 0, defaultStreamBufferCapacity)
			}
		}
		*b.pooled = pooledData[:0]
		streamBufferPool.Put(b.pooled)
	}
	b.data = nil
	b.pooled = nil
	b.read = 0
}

func (b *StreamBuffer) prepareAppend() {
	switch {
	case b.data == nil:
		*b = NewStreamBuffer(defaultStreamBufferCapacity)
	case b.read == 0:
		return
	case b.read >= len(b.data):
		b.data = b.data[:0]
		b.read = 0
	default:
		copy(b.data, b.data[b.read:])
		b.data = b.data[:len(b.data)-b.read]
		b.read = 0
	}
}
