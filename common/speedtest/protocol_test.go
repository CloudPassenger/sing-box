package speedtest

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDownloadRequestRoundTrip(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeDownloadRequest(&buffer, 12345))
	var requestType [1]byte
	_, err := io.ReadFull(&buffer, requestType[:])
	require.NoError(t, err)
	require.Equal(t, byte(TypeDownload), requestType[0])
	length, err := readDownloadRequest(&buffer)
	require.NoError(t, err)
	require.Equal(t, uint32(12345), length)
}

func TestUploadRequestRoundTrip(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeUploadRequest(&buffer, 65536))
	var requestType [1]byte
	_, err := io.ReadFull(&buffer, requestType[:])
	require.NoError(t, err)
	require.Equal(t, byte(TypeUpload), requestType[0])
	length, err := readUploadRequest(&buffer)
	require.NoError(t, err)
	require.Equal(t, uint32(65536), length)
}

func TestResponseRoundTripOk(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeResponse(&buffer, true, []byte(StatusOk.String())))
	ok, message, err := readResponse(&buffer)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "OK", string(message))
}

func TestResponseRoundTripReject(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeResponse(&buffer, false, []byte(StatusError.String())))
	ok, message, err := readResponse(&buffer)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "Disallow", string(message))
}

func TestResponseEmptyMessage(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeResponse(&buffer, true, nil))
	ok, message, err := readResponse(&buffer)
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, message)
}

func TestResponseShortRead(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeResponse(&buffer, true, []byte("OK")))
	// Truncate the buffer to simulate a short read: keep only the status byte.
	truncated := bytes.NewReader(buffer.Bytes()[:1])
	_, _, err := readResponse(truncated)
	require.Error(t, err)
}

func TestUploadSummaryRoundTrip(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, writeUploadSummary(&buffer, 1500*time.Millisecond, 65536))
	duration, length, err := readUploadSummary(&buffer)
	require.NoError(t, err)
	require.Equal(t, 1500*time.Millisecond, duration)
	require.Equal(t, uint32(65536), length)
}

// shortWriter writes at most maxPerCall bytes per Write call, to exercise
// the writeFull loop.
type shortWriter struct {
	buf        bytes.Buffer
	maxPerCall int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxPerCall {
		p = p[:w.maxPerCall]
	}
	return w.buf.Write(p)
}

func TestWriteFullShortWrites(t *testing.T) {
	t.Parallel()
	writer := &shortWriter{maxPerCall: 3}
	payload := []byte("0123456789")
	n, err := writeFull(writer, payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Equal(t, payload, writer.buf.Bytes())
}

func TestStatusString(t *testing.T) {
	t.Parallel()
	require.Equal(t, "OK", StatusOk.String())
	require.Equal(t, "Disallow", StatusError.String())
	require.Equal(t, "2", Status(2).String())
}
