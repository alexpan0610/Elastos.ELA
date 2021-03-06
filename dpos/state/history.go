package state

import "fmt"

// change holds a change and it's rollback function.
type change struct {
	// execute is the function makes state change.
	execute func()

	// rollback is the function rollbacks the change.
	rollback func()
}

// heightChanges holds all changes on a particular height.
type heightChanges struct {
	// height is the height the changes happened.
	height uint32

	// changes are the changes on the height.
	changes []change
}

// append add a change into changes
func (hc *heightChanges) append(c func(), r func()) {
	hc.changes = append(hc.changes, change{execute: c, rollback: r})
}

// commit execute the changes on the height.
func (hc *heightChanges) commit() {
	for _, change := range hc.changes {
		change.execute()
	}
}

// rollback cancel the changes on the height.
func (hc *heightChanges) rollback() {
	for _, change := range hc.changes {
		change.rollback()
	}
}

// history is a helper to log all producers and votes changes for state, so we
// can handle block rollback by tracing the change history, no need to loop
// though all transactions since the beginning of DPOS consensus.
type history struct {
	// capacity is the max block changes stored by history.
	capacity int

	// height is the best height the history knows.
	height uint32

	// changes holds the changes by the height where the changes happens.
	changes []heightChanges

	// cachedChanges holds the changes that not committed yet.
	cachedChanges *heightChanges

	// tempChanges stores the temporary changes caused by illegal block evidence
	// .  The changes will be rollback when next block comes.
	tempChanges []change

	// seekHeight stores a seek height if seekTo method was called, when a new
	// block received, state will be seek to best height first.
	seekHeight uint32
}

// append add a change and it's rollback into history.
func (h *history) append(height uint32, execute func(), rollback func()) {
	// if height==0 means this is a temporary change.
	if height == 0 {
		change := change{execute: execute, rollback: rollback}
		h.tempChanges = append(h.tempChanges, change)
		return
	}

	// rollback and reset tempChanges when next block comes.
	if len(h.tempChanges) > 0 {
		for _, change := range h.tempChanges {
			change.rollback()
		}
		h.tempChanges = nil
	}

	// if cached changes not created, create a new cache instance.
	if h.cachedChanges == nil {
		if h.height != 0 && height != h.height+1 {
			errMsg := fmt.Errorf("state history not continuously,"+
				" expect %d got %d", h.height+1, height)
			panic(errMsg)
		}
		h.cachedChanges = &heightChanges{height: height}
	}

	// changes on one height must be committed or cleared together.
	if height != h.cachedChanges.height {
		panic("previous changes not committed or cleared, wrong usage")
	}

	// append change into cache.
	h.cachedChanges.append(execute, rollback)
}

// commit saves the pending changes into state.
func (h *history) commit(height uint32) {
	// if there are temporary changes, just commit them and return.
	if len(h.tempChanges) > 0 {
		for _, change := range h.tempChanges {
			change.execute()
		}
		return
	}

	// if history on a seek height, seek state to best height first.
	seek := h.height - h.seekHeight
	length := len(h.changes)
	for i := length - int(seek); i >= 0 && i < length; i++ {
		h.changes[i].commit()
	}

	// if changes overflows history's capacity, remove the oldest change.
	if len(h.changes) >= h.capacity {
		h.changes = h.changes[1:]
	}

	// if no cached changes, create an empty height change.
	if h.cachedChanges == nil {
		h.cachedChanges = &heightChanges{height: height}
	}

	// commit cached changes and update history height.
	h.cachedChanges.commit()
	h.height = height
	h.seekHeight = height

	// add change into history.
	h.changes = append(h.changes, *h.cachedChanges)

	// reset cached changes.
	h.cachedChanges = nil
}

// seekTo changes state to a historical height in range of history capacity.
func (h *history) seekTo(height uint32) error {
	// check whether history is enough to seek
	limitHeight := h.height - uint32(len(h.changes))
	if height < limitHeight {
		return fmt.Errorf("seek to %d overflow history capacity,"+
			" at most seek to %d", height, limitHeight)
	}

	// seek changes to the historical height.
	seek := int(h.seekHeight) - int(height)
	length := len(h.changes)
	if seek >= 0 {
		for i := length - 1; i >= length-seek; i-- {
			h.changes[i].rollback()
		}
	} else {
		for i := length + seek; i >= 0 && i < length; i++ {
			h.changes[i].commit()
		}
	}
	h.seekHeight = height

	return nil
}

// rollbackTo restores state to height, and remove all histories after height.
// If no enough histories to rollback return error.
func (h *history) rollbackTo(height uint32) error {
	// check whether history is enough for rollback
	limitHeight := h.height - uint32(len(h.changes))
	if height < limitHeight {
		return fmt.Errorf("rollback to %d overflow history capacity,"+
			" at most rollback to %d", height, limitHeight)
	}

	// rollback and reset tempChanges before rollback.
	if len(h.tempChanges) > 0 {
		for _, change := range h.tempChanges {
			change.rollback()
		}
		h.tempChanges = nil
	}

	// rollback from last history.
	for h.height > height {
		h.changes[len(h.changes)-1].rollback()
		h.changes = h.changes[:len(h.changes)-1]
		h.height = h.height - 1
	}

	return nil
}

// newHistory creates a new history instance.
func newHistory(cap int) *history {
	return &history{
		capacity: cap,
		changes:  make([]heightChanges, 0, cap),
	}
}
