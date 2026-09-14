package streaming

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamBufferReadConsumeAndAppend(t *testing.T) {
	buffer := NewStreamBuffer(8)
	defer buffer.Release()

	buffer.AppendString("hello")

	out := make([]byte, 2)
	n := buffer.Read(out)
	require.Equal(t, 2, n)
	require.Equal(t, "he", string(out))

	buffer.AppendString(" world")
	got := string(buffer.Unread())
	require.Equal(t, "llo world", got)

	buffer.Consume(4)
	got = string(buffer.Unread())
	require.Equal(t, "world", got)
}

func TestStreamBufferReleaseIsIdempotent(t *testing.T) {
	buffer := NewStreamBuffer(8)
	buffer.AppendString("data")

	buffer.Release()
	buffer.Release()
	require.Zero(t, buffer.Len())
	require.Nil(t, buffer.Unread(), "Unread() after Release()")
}

func TestStreamBufferReleaseKeepsOriginalPooledSliceAfterGrowth(t *testing.T) {
	buffer := NewStreamBuffer(8)
	pooled := buffer.pooled
	require.NotNil(t, pooled)

	originalCap := cap(*pooled)

	buffer.AppendString(strings.Repeat("x", maxPooledStreamBufferSize+1))
	require.Greater(t, cap(buffer.data), maxPooledStreamBufferSize)

	buffer.Release()

	require.NotNil(t, *pooled)
	got := cap(*pooled)
	require.Equal(t, originalCap, got)
}

func TestStreamBufferReleaseRecyclesGrownBuffer(t *testing.T) {
	buffer := NewStreamBuffer(0)
	pooled := buffer.pooled
	initialCap := cap(*pooled)

	buffer.AppendString(strings.Repeat("x", defaultStreamBufferCapacity*4))
	grownCap := cap(buffer.data)
	require.Greater(t, grownCap, initialCap)
	require.LessOrEqual(t, grownCap, maxPooledStreamBufferSize)

	buffer.Release()
	got := cap(*pooled)
	require.Equal(t, grownCap, got)
	got = len(*pooled)
	require.Equal(t, 0, got)
}

func TestNewStreamBufferKeepsPoolSlotInSyncWithInitialCapacity(t *testing.T) {
	buffer := NewStreamBuffer(4 * defaultStreamBufferCapacity)
	got := cap(buffer.data)
	require.GreaterOrEqual(t, got, 4*defaultStreamBufferCapacity)

	require.Equal(t, cap(buffer.data), cap(*buffer.pooled), "the pool slot must track the buffer actually allocated")
	buffer.Release()
}
