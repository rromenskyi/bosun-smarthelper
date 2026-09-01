package sandbox

import "testing"

func TestTruncatingBufferKeepsOutputUnderLimit(t *testing.T) {
	var b truncatingBuffer
	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
	if b.buf.String() != "hello" {
		t.Errorf("buf = %q, want %q", b.buf.String(), "hello")
	}
	if b.truncated {
		t.Error("truncated = true, want false for output under the limit")
	}
}

func TestTruncatingBufferDropsOutputPastLimit(t *testing.T) {
	var b truncatingBuffer

	first := make([]byte, maxCapturedOutputBytes-10)
	for i := range first {
		first[i] = 'a'
	}
	if n, err := b.Write(first); err != nil || n != len(first) {
		t.Fatalf("first write: n=%d err=%v", n, err)
	}
	if b.truncated {
		t.Fatal("truncated = true after writing exactly up to the limit's edge")
	}

	// This write straddles the limit: 5 bytes fit, 15 don't.
	second := []byte("0123456789ABCDE")
	n, err := b.Write(second)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	// Write must report success for the full slice — cmd.Run must never
	// see a write error just because the program was chatty.
	if n != len(second) {
		t.Errorf("n = %d, want %d (a truncating write still reports full success)", n, len(second))
	}
	if !b.truncated {
		t.Error("truncated = false, want true once output exceeds the limit")
	}
	if b.buf.Len() != maxCapturedOutputBytes {
		t.Errorf("buf.Len() = %d, want exactly %d", b.buf.Len(), maxCapturedOutputBytes)
	}
	if b.buf.String()[b.buf.Len()-10:] != "0123456789" {
		t.Errorf("tail of captured output = %q, want the first 10 bytes of the straddling write", b.buf.String()[b.buf.Len()-10:])
	}

	// A further write past the limit is entirely dropped but still
	// reports success and keeps the truncated flag set.
	n, err = b.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Errorf("write past limit: n=%d err=%v, want n=4 err=nil", n, err)
	}
	if b.buf.Len() != maxCapturedOutputBytes {
		t.Errorf("buf.Len() = %d after a write past the limit, want it to stay at %d", b.buf.Len(), maxCapturedOutputBytes)
	}
}
