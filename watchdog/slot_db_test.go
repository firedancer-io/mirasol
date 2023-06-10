package watchdog

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Unit_slotBoard(t *testing.T) {

	const numberOfNodes int = 5
	board := newSlotBoard(numberOfNodes)
	const slotBase uint64 = 12345

	// Ensure board is empty.
	require.Empty(t, board, "board should be empty")

	// Populate board.
	highestSlot := uint64(0)
	for i := 0; i < numberOfNodes; i++ {
		nodeID := fmt.Sprintf("node-%d", i)
		board.Set(nodeID, slotBase+uint64(i))
		highestSlot = slotBase + uint64(i)
	}
	// Assert board is not empty.
	require.NotEmpty(t, board, "board should not be empty")
	// And that is has the correct length.
	require.Len(t, board, numberOfNodes)
	// Assert the contents are as expected.
	for i := 0; i < numberOfNodes; i++ {
		nodeID := fmt.Sprintf("node-%d", i)
		require.Equal(t, slotBase+uint64(i), board.Get(nodeID))
	}

	// Assert unknown nodeID returns slot height zero.
	require.Equal(t, uint64(0), board.Get("unknown"))

	// Assert highest slot.
	require.Equal(t, highestSlot, board.HighestSlot())

	// Assert WriteTo.
	var buf bytes.Buffer
	totalWritten, err := board.WriteTo(&buf)
	require.NoError(t, err)
	require.Greater(t, totalWritten, int64(0))

	// Assert Reset.
	require.Len(t, board, numberOfNodes)
	board.Reset()
	require.Len(t, board, 0)
}
