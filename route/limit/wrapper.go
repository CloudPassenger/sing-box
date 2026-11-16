package limit

import (
	"context"
	"net"
	"sync"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/time/rate"
)

type closeHandlerConn struct {
	N.ExtendedConn
	onClose func()
	once    sync.Once
}

func (c *closeHandlerConn) Upstream() any {
	return c.ExtendedConn
}

func (c *closeHandlerConn) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.ExtendedConn.Close()
}

type closeHandlerPacketConn struct {
	N.PacketConn
	onClose func()
	once    sync.Once
}

func (c *closeHandlerPacketConn) Upstream() any {
	return c.PacketConn
}

func (c *closeHandlerPacketConn) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.PacketConn.Close()
}

type trafficLimitedConn struct {
	N.ExtendedConn
	ctx     context.Context
	down    *rate.Limiter
	up      *rate.Limiter
	total   *rate.Limiter
	onClose func()
	once    sync.Once
}

func (c *trafficLimitedConn) Upstream() any {
	return c.ExtendedConn
}

func (c *trafficLimitedConn) Read(p []byte) (n int, err error) {
	limiters := []*rate.Limiter{c.up, c.total}
	burst := limiterBurst(limiters...)
	if burst > 0 && len(p) > burst {
		p = p[:burst]
	}
	n, err = c.ExtendedConn.Read(p)
	if err != nil {
		return
	}
	err = waitLimiters(c.ctx, n, limiters...)
	return
}

func (c *trafficLimitedConn) Write(p []byte) (n int, err error) {
	limiters := []*rate.Limiter{c.down, c.total}
	return writeLimited(c.ctx, c.ExtendedConn, p, limiters...)
}

func (c *trafficLimitedConn) ReadBuffer(buffer *buf.Buffer) error {
	err := c.ExtendedConn.ReadBuffer(buffer)
	if err != nil {
		return err
	}
	return waitLimiters(c.ctx, buffer.Len(), c.up, c.total)
}

func (c *trafficLimitedConn) WriteBuffer(buffer *buf.Buffer) error {
	if err := waitLimiters(c.ctx, buffer.Len(), c.down, c.total); err != nil {
		return err
	}
	return c.ExtendedConn.WriteBuffer(buffer)
}

func (c *trafficLimitedConn) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.ExtendedConn.Close()
}

type trafficLimitedPacketConn struct {
	N.PacketConn
	ctx     context.Context
	down    *rate.Limiter
	up      *rate.Limiter
	total   *rate.Limiter
	onClose func()
	once    sync.Once
}

func (c *trafficLimitedPacketConn) Upstream() any {
	return c.PacketConn
}

func (c *trafficLimitedPacketConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	destination, err = c.PacketConn.ReadPacket(buffer)
	if err != nil {
		return
	}
	err = waitLimiters(c.ctx, buffer.Len(), c.up, c.total)
	return
}

func (c *trafficLimitedPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if err := waitLimiters(c.ctx, buffer.Len(), c.down, c.total); err != nil {
		return err
	}
	return c.PacketConn.WritePacket(buffer, destination)
}

func (c *trafficLimitedPacketConn) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.PacketConn.Close()
}

func limiterBurst(limiters ...*rate.Limiter) int {
	burst := 0
	for _, limiter := range limiters {
		if limiter == nil {
			continue
		}
		if burst == 0 || limiter.Burst() < burst {
			burst = limiter.Burst()
		}
	}
	return burst
}

func waitLimiters(ctx context.Context, n int, limiters ...*rate.Limiter) error {
	for _, limiter := range limiters {
		if limiter == nil {
			continue
		}
		remaining := n
		burst := limiter.Burst()
		if burst <= 0 {
			burst = remaining
		}
		for remaining > 0 {
			chunk := min(remaining, burst)
			err := limiter.WaitN(ctx, chunk)
			if err != nil {
				return err
			}
			remaining -= chunk
		}
	}
	return nil
}

func writeLimited(ctx context.Context, conn net.Conn, p []byte, limiters ...*rate.Limiter) (n int, err error) {
	burst := limiterBurst(limiters...)
	if burst == 0 {
		return conn.Write(p)
	}
	for len(p) > 0 {
		end := min(len(p), burst)
		err = waitLimiters(ctx, end, limiters...)
		if err != nil {
			return
		}
		var nn int
		nn, err = conn.Write(p[:end])
		n += nn
		if err != nil {
			return
		}
		p = p[end:]
	}
	return
}
