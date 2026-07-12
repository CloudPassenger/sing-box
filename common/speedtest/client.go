package speedtest

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/ntp"
)

// SpeedCallback is the callback of a speed test.
//
// When end is false:
// duration is the time since the last invocation and transferred is the
// new bytes transferred since the last invocation.
//
// When end is true:
// duration and transferred describe the whole test.
type SpeedCallback func(duration time.Duration, transferred uint32, end bool)

// DownloadTest requests length bytes of random data from conn and reports
// progress through callback. conn is closed when the test finishes.
func DownloadTest(ctx context.Context, conn net.Conn, length uint32, callback SpeedCallback) error {
	defer conn.Close()

	err := writeDownloadRequest(conn, length)
	if err != nil {
		return E.Cause(err, "write download request")
	}
	permitted, message, err := readResponse(conn)
	if err != nil {
		return E.Cause(err, "read download response")
	}
	if !permitted {
		return E.New("download test is forbidden: ", string(message))
	}

	timeFunc := ntp.TimeFuncFromContext(ctx)
	if timeFunc == nil {
		timeFunc = time.Now
	}

	done := make(chan error, 1)
	go func() {
		var transferred uint32
		started := timeFunc()
		lastUpdate := started
		buffer := make([]byte, chunkSize)
		remaining := length
		for remaining > 0 {
			readSize := min(remaining, chunkSize)
			n, readErr := io.ReadFull(conn, buffer[:readSize])
			if n > 0 {
				now := timeFunc()
				transferred += uint32(n)
				remaining -= uint32(n)
				callback(now.Sub(lastUpdate), uint32(n), false)
				lastUpdate = now
			}
			if readErr != nil {
				done <- E.Cause(readErr, "read download data")
				return
			}
		}
		callback(timeFunc().Sub(started), transferred, true)
		done <- nil
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-done:
		if ctxErr := ctx.Err(); ctxErr != nil && E.IsClosedOrCanceled(err) {
			return ctxErr
		}
		return err
	}
}

// UploadTest sends length bytes of random data to conn and reports progress
// through callback. conn is closed when the test finishes.
func UploadTest(ctx context.Context, conn net.Conn, length uint32, callback SpeedCallback) error {
	defer conn.Close()

	err := writeUploadRequest(conn, length)
	if err != nil {
		return E.Cause(err, "write upload request")
	}
	permitted, message, err := readResponse(conn)
	if err != nil {
		return E.Cause(err, "read upload response")
	}
	if !permitted {
		return E.New("upload test is forbidden: ", string(message))
	}

	timeFunc := ntp.TimeFuncFromContext(ctx)
	if timeFunc == nil {
		timeFunc = time.Now
	}

	done := make(chan error, 1)
	go func() {
		chunk := make([]byte, chunkSize)
		_, randErr := rand.Read(chunk)
		if randErr != nil {
			done <- E.Cause(randErr, "generate upload data")
			return
		}

		lastUpdate := timeFunc()
		remaining := length
		for remaining > 0 {
			writeSize := min(remaining, chunkSize)
			n, writeErr := writeFull(conn, chunk[:writeSize])
			if n > 0 {
				now := timeFunc()
				remaining -= uint32(n)
				callback(now.Sub(lastUpdate), uint32(n), false)
				lastUpdate = now
			}
			if writeErr != nil {
				done <- E.Cause(writeErr, "write upload data")
				return
			}
		}

		duration, totalReceived, summaryErr := readUploadSummary(conn)
		if summaryErr != nil {
			done <- E.Cause(summaryErr, "read upload summary")
			return
		}
		callback(duration, totalReceived, true)
		done <- nil
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-done:
		if ctxErr := ctx.Err(); ctxErr != nil && E.IsClosedOrCanceled(err) {
			return ctxErr
		}
		return err
	}
}
