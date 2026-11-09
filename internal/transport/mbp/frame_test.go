package mbp

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestFrameReadWriteRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		frame *Frame
	}{
		{
			name: "empty payload",
			frame: &Frame{
				Version:       0x01,
				Type:          TypeHello,
				Flags:         0,
				PayloadLength: 0,
				CorrelationID: 12345,
				Payload:       []byte{},
			},
		},
		{
			name: "small payload",
			frame: &Frame{
				Version:       0x01,
				Type:          TypeWrite,
				Flags:         0,
				PayloadLength: 5,
				CorrelationID: 67890,
				Payload:       []byte("hello"),
			},
		},
		{
			name: "with flags",
			frame: &Frame{
				Version:       0x01,
				Type:          TypeActivateResp,
				Flags:         FlagCompressed | FlagStreaming,
				PayloadLength: 4,
				CorrelationID: 99999,
				Payload:       []byte("test"),
			},
		},
		{
			name: "large payload",
			frame: &Frame{
				Version:       0x01,
				Type:          TypeRead,
				Flags:         0,
				PayloadLength: 1000,
				CorrelationID: 11111,
				Payload:       bytes.Repeat([]byte("x"), 1000),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write frame to buffer
			buf := &bytes.Buffer{}
			if err := WriteFrame(buf, tt.frame); err != nil {
				t.Fatalf("WriteFrame failed: %v", err)
			}

			// Read frame back from buffer
			reader := bytes.NewReader(buf.Bytes())
			readFrame, err := ReadFrame(reader)
			if err != nil {
				t.Fatalf("ReadFrame failed: %v", err)
			}

			// Compare all fields
			if readFrame.Version != tt.frame.Version {
				t.Errorf("Version mismatch: got %d, want %d", readFrame.Version, tt.frame.Version)
			}
			if readFrame.Type != tt.frame.Type {
				t.Errorf("Type mismatch: got %d, want %d", readFrame.Type, tt.frame.Type)
			}
			if readFrame.Flags != tt.frame.Flags {
				t.Errorf("Flags mismatch: got %d, want %d", readFrame.Flags, tt.frame.Flags)
			}
			if readFrame.PayloadLength != tt.frame.PayloadLength {
				t.Errorf("PayloadLength mismatch: got %d, want %d", readFrame.PayloadLength, tt.frame.PayloadLength)
			}
			if readFrame.CorrelationID != tt.frame.CorrelationID {
				t.Errorf("CorrelationID mismatch: got %d, want %d", readFrame.CorrelationID, tt.frame.CorrelationID)
			}
			if !bytes.Equal(readFrame.Payload, tt.frame.Payload) {
				t.Errorf("Payload mismatch: got %v, want %v", readFrame.Payload, tt.frame.Payload)
			}
		})
	}
}

func TestFramePayloadSizeLimit(t *testing.T) {
	// Create frame with payload exceeding 16MB
	frame := &Frame{
		Version:       0x01,
		Type:          TypeWrite,
		Flags:         0,
		PayloadLength: MaxPayloadSize + 1,
		CorrelationID: 12345,
		Payload:       make([]byte, MaxPayloadSize+1),
	}

	buf := &bytes.Buffer{}
	err := WriteFrame(buf, frame)
	if err != ErrPayloadTooLarge {
		t.Errorf("expected ErrPayloadTooLarge, got %v", err)
	}
}

// TestReadFrameLimited_PreAuthCap: the HELLO frame is read before any
// credential exists, so an announced payload above PreAuthMaxPayloadSize must
// be refused before the payload buffer is allocated.
func TestReadFrameLimited_PreAuthCap(t *testing.T) {
	h := make([]byte, FramePrefixSize)
	h[0] = 0x01
	h[1] = TypeHello
	binary.BigEndian.PutUint32(h[4:8], PreAuthMaxPayloadSize+1)

	if _, err := readFrameLimited(bytes.NewReader(h), PreAuthMaxPayloadSize); err != ErrPayloadTooLarge {
		t.Errorf("expected ErrPayloadTooLarge, got %v", err)
	}

	// A frame exactly at the cap must still pass the length check: the
	// payload read then fails on EOF, which proves the buffer was allocated.
	h2 := make([]byte, FramePrefixSize)
	h2[0] = 0x01
	h2[1] = TypeHello
	binary.BigEndian.PutUint32(h2[4:8], PreAuthMaxPayloadSize)
	if _, err := readFrameLimited(bytes.NewReader(h2), PreAuthMaxPayloadSize); err == nil {
		t.Error("expected an EOF error reading the missing payload, got nil")
	}
}

func TestFrameVersionValidation(t *testing.T) {
	// Write frame with invalid version
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x02, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	reader := bytes.NewReader(buf.Bytes())
	_, err := ReadFrame(reader)
	if err != ErrVersionMismatch {
		t.Errorf("expected ErrVersionMismatch, got %v", err)
	}
}

func TestFrameIncompleteRead(t *testing.T) {
	// Partial frame header
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x01, 0x02})

	reader := bytes.NewReader(buf.Bytes())
	_, err := ReadFrame(reader)
	if err != io.EOF && err != io.ErrUnexpectedEOF {
		t.Errorf("expected EOF or ErrUnexpectedEOF, got %v", err)
	}
}
