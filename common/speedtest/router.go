package speedtest

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

var _ adapter.ConnectionRouterEx = (*Router)(nil)

type handleOption uint8

const (
	handleAllow handleOption = iota
	handleReject
)

// NewRouter wraps router with private speedtest handling according to
// option, which is the raw `speed_test` inbound field value.
//
// An empty value or "disable" returns router unchanged. "allow" and
// "reject" install the speedtest interception. Any other value is a
// configuration error.
func NewRouter(router adapter.ConnectionRouterEx, logger logger.ContextLogger, option string) (adapter.ConnectionRouterEx, error) {
	var mode handleOption
	switch option {
	case "", "disable":
		return router, nil
	case "allow":
		mode = handleAllow
	case "reject":
		mode = handleReject
	default:
		return nil, E.New("unknown speed_test mode: ", option)
	}
	return &Router{router, logger, mode}, nil
}

type Router struct {
	router adapter.ConnectionRouterEx
	logger logger.ContextLogger
	mode   handleOption
}

func (r *Router) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	if metadata.Destination.Fqdn != MagicAddress {
		return r.router.RouteConnection(ctx, conn, metadata)
	}
	err := r.speedTest(ctx, conn, metadata.Source)
	if err != nil {
		r.logger.WarnContext(ctx, "speedtest connection: ", err)
	}
	return nil
}

func (r *Router) RoutePacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	return r.router.RoutePacketConnection(ctx, conn, metadata)
}

func (r *Router) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if metadata.Destination.Fqdn != MagicAddress {
		r.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	err := r.speedTest(ctx, conn, metadata.Source)
	if err != nil && !E.IsClosedOrCanceled(err) {
		r.logger.ErrorContext(ctx, "speedtest connection: ", err)
	}
	_ = conn.Close()
	if onClose != nil {
		onClose(err)
	}
}

func (r *Router) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (r *Router) Upstream() any {
	return r.router
}

func (r *Router) speedTest(ctx context.Context, conn net.Conn, source M.Socksaddr) error {
	r.logger.InfoContext(ctx, "inbound speedtest connection from ", source)
	var requestType [1]byte
	_, err := io.ReadFull(conn, requestType[:])
	if err != nil {
		return E.Cause(err, "read speedtest request type")
	}
	switch requestType[0] {
	case TypeDownload:
		return r.downloadTest(ctx, conn)
	case TypeUpload:
		return r.uploadTest(ctx, conn)
	default:
		return E.New("unknown speedtest request type: ", requestType[0])
	}
}

func (r *Router) downloadTest(ctx context.Context, conn net.Conn) error {
	if r.mode == handleReject {
		err := writeResponse(conn, false, []byte(StatusError.String()))
		if err != nil {
			return E.Cause(err, "write reject download response")
		}
		return nil
	}
	length, err := readDownloadRequest(conn)
	if err != nil {
		return err
	}
	err = writeResponse(conn, true, []byte(StatusOk.String()))
	if err != nil {
		return E.Cause(err, "write download response")
	}

	chunk := make([]byte, chunkSize)
	_, err = rand.Read(chunk)
	if err != nil {
		return E.Cause(err, "generate download data")
	}

	remaining := length
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		writeSize := min(remaining, chunkSize)
		_, err = writeFull(conn, chunk[:writeSize])
		if err != nil {
			return E.Cause(err, "write download data")
		}
		remaining -= writeSize
	}
	return nil
}

func (r *Router) uploadTest(ctx context.Context, conn net.Conn) error {
	if r.mode == handleReject {
		err := writeResponse(conn, false, []byte(StatusError.String()))
		if err != nil {
			return E.Cause(err, "write reject upload response")
		}
		return nil
	}
	length, err := readUploadRequest(conn)
	if err != nil {
		return err
	}
	err = writeResponse(conn, true, []byte(StatusOk.String()))
	if err != nil {
		return E.Cause(err, "write upload response")
	}

	timeFunc := ntp.TimeFuncFromContext(ctx)
	if timeFunc == nil {
		timeFunc = time.Now
	}
	start := timeFunc()

	buffer := make([]byte, chunkSize)
	var received uint32
	remaining := length
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		readSize := min(remaining, chunkSize)
		n, readErr := io.ReadFull(conn, buffer[:readSize])
		received += uint32(n)
		remaining -= uint32(n)
		if readErr != nil {
			return E.Cause(readErr, "read upload data")
		}
	}
	return writeUploadSummary(conn, timeFunc().Sub(start), received)
}
