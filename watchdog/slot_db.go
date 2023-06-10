package watchdog

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"text/tabwriter"
)

// slotBoard is a concurrency-safe map:
// - key: a node identity
// - value: the latest observed slot height
type slotBoard map[string]uint64

var mutexSlotBoard *sync.RWMutex = &sync.RWMutex{}

func newSlotBoard(numberOfNodes int) slotBoard {
	return make(slotBoard, numberOfNodes+1)
}

// Get returns the slot number which key is nodeID.
// If key nodeID can't be found, Get returns -1.
func (board slotBoard) Get(nodeID string) uint64 {
	mutexSlotBoard.RLock()
	defer mutexSlotBoard.RUnlock()

	// Ensure an entry for nodeID exists.
	return board[nodeID]
}

// Set sets the slot number for a certain nodeID.
func (board slotBoard) Set(nodeID string, number uint64) {
	// TODO if number < board.internal[nodeID] it may mean the node
	// has forked. Got to check this w/ ripatel and decide what's the
	// desired behavior.
	mutexSlotBoard.Lock()
	defer mutexSlotBoard.Unlock()
	board[nodeID] = number
}

// Reset empties the slot board.
func (board slotBoard) Reset() {
	mutexSlotBoard.Lock()
	defer mutexSlotBoard.Unlock()
	for k := range board {
		delete(board, k)
	}
}

// HighestSlot returns highest slot number.
func (board slotBoard) HighestSlot() uint64 {
	mutexSlotBoard.RLock()
	defer mutexSlotBoard.RUnlock()
	highest := uint64(0)
	for _, slotHeight := range board {
		if slotHeight > highest {
			highest = slotHeight
		}
	}
	return highest
}

// WriteTo prints a table with each known node identity and respective slot
// height.
func (board slotBoard) WriteTo(out io.Writer) (int64, error) {
	if len(board) == 0 {
		return 0, nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 1, ' ', tabwriter.AlignRight|tabwriter.Debug)
	totalWritten, err := fmt.Fprintln(w, "Node ID\tSlot Height")
	if err != nil {
		return int64(totalWritten), err
	}
	mutexSlotBoard.RLock()
	// Sort by slot height, higher to lower.

	// First, we need a reverse lookup map.
	// NOTE: multiple nodes may share the same slot height, and we account
	// for that.
	reverse := make(map[uint64][]string, len(board))
	for nodeID, slotHeight := range board {
		reverse[slotHeight] = append(reverse[slotHeight], nodeID)
	}
	// A new slice of uint64 is required for sorting.
	slotHeights := make([]uint64, 0, len(board))
	mutexSlotBoard.RUnlock()
	for slotHeight := range reverse {
		slotHeights = append(slotHeights, slotHeight)
	}
	sort.Slice(slotHeights, func(i, j int) bool {
		return slotHeights[i] > slotHeights[j]
	})
	// Print node identities ordered by slot height.
	for _, slotHeight := range slotHeights {
		for _, nodeID := range reverse[slotHeight] {
			written, err := fmt.Fprintf(w, "%s\t%d\n", nodeID, slotHeight)
			totalWritten = totalWritten + written
			if err != nil {
				return int64(totalWritten), err
			}
		}
	}

	return int64(totalWritten), w.Flush()
}
