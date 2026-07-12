// Package speedtest implements a private speedtest protocol invited by Hysteria 2.
package speedtest

import (
	"encoding/binary"
	"io"
	"strconv"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// MagicAddress is the reserved FQDN destination used to trigger the private speedtest protocol.
	MagicAddress = "@SpeedTest"

	TypeDownload = 0x01
	TypeUpload   = 0x02

	// chunkSize must stay well below 65536: several multiplexed transports
	// (e.g. AnyTLS streams) encode a single Write call's payload length as
	// a uint16, so writing exactly 65536 bytes in one call silently wraps
	// to a zero-length frame and corrupts the stream.
	chunkSize = 32 * 1024
)

type Status byte

const (
	StatusOk    Status = 0x00
	StatusError Status = 0x01
)

func (s Status) String() string {
	switch s {
	case StatusOk:
		return "OK"
	case StatusError:
		return "Disallow"
	default:
		return strconv.Itoa(int(s))
	}
}

// DownloadRequest format:
// Type (byte, 0x01)
// Requested data length (uint32 BE)

func readDownloadRequest(reader io.Reader) (length uint32, err error) {
	err = binary.Read(reader, binary.BigEndian, &length)
	if err != nil {
		err = E.Cause(err, "read download request")
	}
	return
}

func writeDownloadRequest(writer io.Writer, length uint32) error {
	buffer := make([]byte, 5)
	buffer[0] = TypeDownload
	binary.BigEndian.PutUint32(buffer[1:], length)
	_, err := writeFull(writer, buffer)
	if err != nil {
		return E.Cause(err, "write download request")
	}
	return nil
}

// Response format (shared by download and upload):
// Status (byte, 0=ok, 1=error)
// Message length (uint16 BE)
// Message (bytes)

func readResponse(reader io.Reader) (bool, []byte, error) {
	var status [1]byte
	_, err := io.ReadFull(reader, status[:])
	if err != nil {
		return false, nil, E.Cause(err, "read response status")
	}
	var messageLength uint16
	err = binary.Read(reader, binary.BigEndian, &messageLength)
	if err != nil {
		return false, nil, E.Cause(err, "read response message length")
	}
	if messageLength == 0 {
		return Status(status[0]) == StatusOk, []byte{}, nil
	}
	message := make([]byte, messageLength)
	_, err = io.ReadFull(reader, message)
	if err != nil {
		return false, nil, E.Cause(err, "read response message (length: ", messageLength, ")")
	}
	return Status(status[0]) == StatusOk, message, nil
}

func writeResponse(writer io.Writer, ok bool, message []byte) error {
	size := 1 + 2 + len(message)
	buffer := buf.NewSize(size)
	defer buffer.Release()
	if ok {
		common.Must(buffer.WriteByte(byte(StatusOk)))
	} else {
		common.Must(buffer.WriteByte(byte(StatusError)))
	}
	common.Must(binary.Write(buffer, binary.BigEndian, uint16(len(message))))
	common.Must1(buffer.Write(message))
	_, err := writeFull(writer, buffer.Bytes())
	if err != nil {
		return E.Cause(err, "write response")
	}
	return nil
}

// UploadRequest format:
// Type (byte, 0x02)
// Upload data length (uint32 BE)

func readUploadRequest(reader io.Reader) (length uint32, err error) {
	err = binary.Read(reader, binary.BigEndian, &length)
	if err != nil {
		err = E.Cause(err, "read upload request")
	}
	return
}

func writeUploadRequest(writer io.Writer, length uint32) error {
	buffer := make([]byte, 5)
	buffer[0] = TypeUpload
	binary.BigEndian.PutUint32(buffer[1:], length)
	_, err := writeFull(writer, buffer)
	if err != nil {
		return E.Cause(err, "write upload request")
	}
	return nil
}

// UploadSummary format (no status prefix):
// Duration (in milliseconds, uint32 BE)
// Received data length (uint32 BE)

func readUploadSummary(reader io.Reader) (time.Duration, uint32, error) {
	var duration uint32
	err := binary.Read(reader, binary.BigEndian, &duration)
	if err != nil {
		return 0, 0, E.Cause(err, "read upload summary duration")
	}
	var length uint32
	err = binary.Read(reader, binary.BigEndian, &length)
	if err != nil {
		return 0, 0, E.Cause(err, "read upload summary length")
	}
	return time.Duration(duration) * time.Millisecond, length, nil
}

func writeUploadSummary(writer io.Writer, duration time.Duration, length uint32) error {
	buffer := make([]byte, 8)
	binary.BigEndian.PutUint32(buffer, uint32(duration/time.Millisecond))
	binary.BigEndian.PutUint32(buffer[4:], length)
	_, err := writeFull(writer, buffer)
	if err != nil {
		return E.Cause(err, "write upload summary")
	}
	return nil
}

// writeFull writes p to writer in full, looping until every byte is written
// or an error occurs. A single Write call is not guaranteed to consume the
// whole buffer.
func writeFull(writer io.Writer, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := writer.Write(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
