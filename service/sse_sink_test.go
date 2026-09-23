package service

import (
	"io"
	"testing"
)

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestSSESinkConvertsShortWriteToError(t *testing.T) {
	sink := newSSESink(shortWriter{}, nil)
	if _, err := sink.Write([]byte("event: x\n\n")); err != io.ErrShortWrite {
		t.Fatalf("short write error=%v, want %v", err, io.ErrShortWrite)
	}
}
