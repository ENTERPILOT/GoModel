package streaming

import (
	"strings"
	"testing"
)

func TestStreamBufferReadConsumeAndAppend(t *testing.T) {
	buffer := NewStreamBuffer(8)
	defer buffer.Release()

	buffer.AppendString("hello")

	out := make([]byte, 2)
	if n := buffer.Read(out); n != 2 {
		t.Fatalf("Read() = %d, want 2", n)
	}
	if string(out) != "he" {
		t.Fatalf("Read() data = %q, want %q", string(out), "he")
	}

	buffer.AppendString(" world")
	if got := string(buffer.Unread()); got != "llo world" {
		t.Fatalf("Unread() = %q, want %q", got, "llo world")
	}

	buffer.Consume(4)
	if got := string(buffer.Unread()); got != "world" {
		t.Fatalf("Unread() after Consume() = %q, want %q", got, "world")
	}
}

func TestStreamBufferReleaseIsIdempotent(t *testing.T) {
	buffer := NewStreamBuffer(8)
	buffer.AppendString("data")

	buffer.Release()
	buffer.Release()

	if got := buffer.Len(); got != 0 {
		t.Fatalf("Len() after Release() = %d, want 0", got)
	}
	if got := buffer.Unread(); got != nil {
		t.Fatalf("Unread() after Release() = %v, want nil", got)
	}
}

func TestStreamBufferReleaseKeepsOriginalPooledSliceAfterGrowth(t *testing.T) {
	buffer := NewStreamBuffer(8)
	pooled := buffer.pooled
	if pooled == nil {
		t.Fatal("pooled = nil, want original pooled handle")
	}
	originalCap := cap(*pooled)

	buffer.AppendString(strings.Repeat("x", maxPooledStreamBufferSize+1))
	if cap(buffer.data) <= maxPooledStreamBufferSize {
		t.Fatalf("active buffer cap = %d, want oversized allocation", cap(buffer.data))
	}

	buffer.Release()

	if *pooled == nil {
		t.Fatal("pooled slice = nil, want original slice returned to pool")
	}
	if got := cap(*pooled); got != originalCap {
		t.Fatalf("pooled cap = %d, want original cap %d", got, originalCap)
	}
}

func TestStreamBufferReleaseRecyclesGrownBuffer(t *testing.T) {
	buffer := NewStreamBuffer(0)
	pooled := buffer.pooled
	initialCap := cap(*pooled)

	buffer.AppendString(strings.Repeat("x", defaultStreamBufferCapacity*4))
	grownCap := cap(buffer.data)
	if grownCap <= initialCap {
		t.Fatalf("active buffer cap = %d, want growth beyond initial %d", grownCap, initialCap)
	}
	if grownCap > maxPooledStreamBufferSize {
		t.Fatalf("test setup: grown cap %d must stay within pool bound %d", grownCap, maxPooledStreamBufferSize)
	}

	buffer.Release()

	if got := cap(*pooled); got != grownCap {
		t.Fatalf("pooled cap = %d, want grown cap %d recycled into the pool", got, grownCap)
	}
	if got := len(*pooled); got != 0 {
		t.Fatalf("pooled len = %d, want 0 after release", got)
	}
}

func TestNewStreamBufferKeepsPoolSlotInSyncWithInitialCapacity(t *testing.T) {
	buffer := NewStreamBuffer(4 * defaultStreamBufferCapacity)
	if got := cap(buffer.data); got < 4*defaultStreamBufferCapacity {
		t.Fatalf("buffer cap = %d, want at least requested %d", got, 4*defaultStreamBufferCapacity)
	}
	if got, want := cap(*buffer.pooled), cap(buffer.data); got != want {
		t.Fatalf("pool slot cap = %d, want %d — the slot must track the buffer actually allocated", got, want)
	}
	buffer.Release()
}
