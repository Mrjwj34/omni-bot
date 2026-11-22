package store

import (
	"encoding/binary"
	"fmt"
	"math"
)

// EncodeFloat32LE serializes a float32 slice into little-endian IEEE-754 bytes for SQLite BLOB storage.
func EncodeFloat32LE(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// DecodeFloat32LE deserializes little-endian IEEE-754 bytes back into a float32 slice.
func DecodeFloat32LE(raw []byte) ([]float32, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("store: invalid float32 blob length %d", len(raw))
	}
	n := len(raw) / 4
	out := make([]float32, n)
	for i := range n {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out, nil
}
