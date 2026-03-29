package display

import "sync"

// BatchDevice wraps any Device and adds BeginBatch/EndBatch methods.
//
// While a batch is active every call to Flush is swallowed (no hardware write
// occurs) but a pending-flush flag is set.  When EndBatch is called, a single
// real flush is issued if at least one Flush was requested during the batch,
// then the device reverts to its normal pass-through behaviour.
//
// This is useful when many draw operations must be committed in one atomic
// hardware update — for example, when updating several sections of the
// display at once — so the screen never shows a partially-updated frame.
//
// All methods are safe for concurrent use.
type BatchDevice struct {
	Device

	mu      sync.Mutex
	active  bool // true between BeginBatch and EndBatch
	pending bool // true if Flush was called while active
}

// NewBatchDevice wraps dev so callers can use BeginBatch/EndBatch.
func NewBatchDevice(dev Device) *BatchDevice {
	return &BatchDevice{Device: dev}
}

// BeginBatch starts a batch.  While the batch is active, Flush calls are
// queued rather than forwarded to the underlying device.  Calling BeginBatch
// while a batch is already active is a no-op.
func (b *BatchDevice) BeginBatch() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = true
}

// EndBatch ends the current batch.  If any Flush was called during the batch,
// EndBatch performs exactly one real flush now.  Calling EndBatch outside an
// active batch is a no-op.
func (b *BatchDevice) EndBatch() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return nil
	}
	b.active = false
	if b.pending {
		b.pending = false
		return b.Device.Flush()
	}
	return nil
}

// Flush forwards to the underlying device unless a batch is active, in which
// case it records a pending flush and returns nil immediately.
func (b *BatchDevice) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active {
		b.pending = true
		return nil
	}
	return b.Device.Flush()
}
