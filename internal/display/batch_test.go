package display

import (
	"errors"
	"image/color"
	"testing"
)

// stubDevice is a minimal Device that counts how many times Flush is called
// and can optionally return an error.
type stubDevice struct {
	w, h       int
	flushCount int
	flushErr   error
}

func (s *stubDevice) Width() int                              { return s.w }
func (s *stubDevice) Height() int                             { return s.h }
func (s *stubDevice) SetPixel(_, _ int, _ color.Color)        {}
func (s *stubDevice) Clear(_ color.Color)                     {}
func (s *stubDevice) DrawHLine(_, _, _ int, _ color.Color)    {}
func (s *stubDevice) DrawRect(_, _, _, _ int, _ color.Color)  {}
func (s *stubDevice) DrawCircle(_, _, _ int, _ color.Color)   {}
func (s *stubDevice) Close() error                            { return nil }
func (s *stubDevice) Flush() error {
	s.flushCount++
	return s.flushErr
}

// TestBatchDevice_FlushOutsideBatch verifies that Flush is forwarded normally
// when no batch is active.
func TestBatchDevice_FlushOutsideBatch(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	if err := bd.Flush(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.flushCount != 1 {
		t.Fatalf("expected 1 flush, got %d", stub.flushCount)
	}
}

// TestBatchDevice_FlushSuppressedDuringBatch verifies that Flush calls inside
// a batch do not reach the underlying device.
func TestBatchDevice_FlushSuppressedDuringBatch(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	bd.BeginBatch()
	for i := 0; i < 5; i++ {
		if err := bd.Flush(); err != nil {
			t.Fatalf("unexpected error during batch flush: %v", err)
		}
	}

	if stub.flushCount != 0 {
		t.Fatalf("expected 0 flushes during batch, got %d", stub.flushCount)
	}
}

// TestBatchDevice_EndBatchFlushesOnce verifies that EndBatch issues exactly
// one hardware flush when Flush was called during the batch.
func TestBatchDevice_EndBatchFlushesOnce(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	bd.BeginBatch()
	_ = bd.Flush()
	_ = bd.Flush()
	_ = bd.Flush()

	if err := bd.EndBatch(); err != nil {
		t.Fatalf("unexpected error from EndBatch: %v", err)
	}
	if stub.flushCount != 1 {
		t.Fatalf("expected exactly 1 flush from EndBatch, got %d", stub.flushCount)
	}
}

// TestBatchDevice_EndBatchNoFlushWhenNoneRequested verifies that EndBatch does
// not flush the hardware if Flush was never called during the batch.
func TestBatchDevice_EndBatchNoFlushWhenNoneRequested(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	bd.BeginBatch()
	// no Flush calls
	if err := bd.EndBatch(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.flushCount != 0 {
		t.Fatalf("expected 0 flushes, got %d", stub.flushCount)
	}
}

// TestBatchDevice_EndBatchOutsideBatch verifies that EndBatch is a no-op when
// called without a prior BeginBatch.
func TestBatchDevice_EndBatchOutsideBatch(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	if err := bd.EndBatch(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.flushCount != 0 {
		t.Fatalf("expected 0 flushes, got %d", stub.flushCount)
	}
}

// TestBatchDevice_FlushNormalAfterEndBatch verifies that after EndBatch,
// subsequent Flush calls reach the underlying device again.
func TestBatchDevice_FlushNormalAfterEndBatch(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	bd.BeginBatch()
	_ = bd.Flush() // suppressed
	_ = bd.EndBatch()   // real flush #1

	_ = bd.Flush() // should reach hardware: flush #2

	if stub.flushCount != 2 {
		t.Fatalf("expected 2 flushes (1 from EndBatch + 1 direct), got %d", stub.flushCount)
	}
}

// TestBatchDevice_EndBatchPropagatesError verifies that an error from the
// underlying Flush is returned by EndBatch.
func TestBatchDevice_EndBatchPropagatesError(t *testing.T) {
	want := errors.New("hardware failure")
	stub := &stubDevice{w: 100, h: 100, flushErr: want}
	bd := NewBatchDevice(stub)

	bd.BeginBatch()
	_ = bd.Flush()
	if err := bd.EndBatch(); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

// TestBatchDevice_BeginBatchIdempotent verifies that calling BeginBatch twice
// does not cause a double-flush on EndBatch.
func TestBatchDevice_BeginBatchIdempotent(t *testing.T) {
	stub := &stubDevice{w: 100, h: 100}
	bd := NewBatchDevice(stub)

	bd.BeginBatch()
	bd.BeginBatch() // second call is a no-op
	_ = bd.Flush()
	_ = bd.EndBatch()

	if stub.flushCount != 1 {
		t.Fatalf("expected 1 flush, got %d", stub.flushCount)
	}
}
