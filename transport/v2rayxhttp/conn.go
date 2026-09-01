package xhttp

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/common/xray/signal/done"
)

type splitConn struct {
	writer     io.WriteCloser
	reader     io.ReadCloser
	remoteAddr net.Addr
	localAddr  net.Addr
	onClose    func()
	closeOnce  sync.Once
	closeErr   error
}

func (c *splitConn) Write(b []byte) (int, error) {
	return c.writer.Write(b)
}

func (c *splitConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (c *splitConn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		c.closeErr = errors.Join(c.writer.Close(), c.reader.Close())
	})
	return c.closeErr
}

func (c *splitConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *splitConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *splitConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

func (c *splitConn) SetReadDeadline(t time.Time) error {
	reader, loaded := c.reader.(interface{ SetReadDeadline(time.Time) error })
	if !loaded {
		return os.ErrInvalid
	}
	return reader.SetReadDeadline(t)
}

func (c *splitConn) SetWriteDeadline(t time.Time) error {
	writer, loaded := c.writer.(interface{ SetWriteDeadline(time.Time) error })
	if !loaded {
		return os.ErrInvalid
	}
	return writer.SetWriteDeadline(t)
}

func (c *splitConn) NeedAdditionalReadDeadline() bool {
	if reader, loaded := c.reader.(interface{ NeedAdditionalReadDeadline() bool }); loaded {
		return reader.NeedAdditionalReadDeadline()
	}
	_, loaded := c.reader.(interface{ SetReadDeadline(time.Time) error })
	return !loaded
}

type interruptibleWriteCloser interface {
	io.WriteCloser
	Interrupt()
}

type deadlineWriteCloser struct {
	writer   interruptibleWriteCloser
	deadline interruptDeadline
}

func newDeadlineWriteCloser(writer interruptibleWriteCloser) *deadlineWriteCloser {
	wrapper := &deadlineWriteCloser{writer: writer}
	wrapper.deadline.interrupt = writer.Interrupt
	return wrapper
}

func (w *deadlineWriteCloser) Write(p []byte) (n int, err error) {
	if w.deadline.isExpired() {
		return 0, os.ErrDeadlineExceeded
	}
	n, err = w.writer.Write(p)
	if err != nil && w.deadline.isExpired() {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (w *deadlineWriteCloser) Close() error {
	w.deadline.close()
	return w.writer.Close()
}

func (w *deadlineWriteCloser) SetWriteDeadline(t time.Time) error {
	return w.deadline.set(t)
}

type pipeInterruptWriter struct {
	*io.PipeWriter
	closeOnce sync.Once
	closeErr  error
}

func (w *pipeInterruptWriter) Interrupt() {
	w.closeOnce.Do(func() {
		w.closeErr = w.CloseWithError(os.ErrDeadlineExceeded)
	})
}

func (w *pipeInterruptWriter) Close() error {
	w.closeOnce.Do(func() {
		w.closeErr = w.PipeWriter.Close()
	})
	return w.closeErr
}

type interruptDeadline struct {
	mu         sync.Mutex
	timer      *time.Timer
	generation uint64
	closed     bool
	expired    bool
	interrupt  func()
}

func (d *interruptDeadline) set(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return net.ErrClosed
	}
	if d.expired {
		return os.ErrDeadlineExceeded
	}
	d.generation++
	generation := d.generation
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() {
		return nil
	}
	duration := time.Until(t)
	if duration > 0 {
		d.timer = time.AfterFunc(duration, func() {
			d.expire(generation)
		})
		return nil
	}
	d.expired = true
	d.interrupt()
	return nil
}

func (d *interruptDeadline) expire(generation uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || generation != d.generation {
		return
	}
	d.timer = nil
	d.expired = true
	d.interrupt()
}

func (d *interruptDeadline) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	d.generation++
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

func (d *interruptDeadline) isExpired() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.expired
}

type H1Conn struct {
	UnreadedResponsesCount int
	RespBufReader          *bufio.Reader
	net.Conn
}

func NewH1Conn(conn net.Conn) *H1Conn {
	return &H1Conn{
		RespBufReader: bufio.NewReader(conn),
		Conn:          conn,
	}
}

type httpServerConn struct {
	sync.Mutex
	*done.Instance
	io.Reader // no need to Close request.Body
	http.ResponseWriter
}

func (c *httpServerConn) Write(b []byte) (int, error) {
	c.Lock()
	defer c.Unlock()
	if c.Done() {
		return 0, io.ErrClosedPipe
	}
	n, err := c.ResponseWriter.Write(b)
	if err == nil {
		c.ResponseWriter.(http.Flusher).Flush()
	}
	return n, err
}

func (c *httpServerConn) SetReadDeadline(t time.Time) error {
	return http.NewResponseController(c.ResponseWriter).SetReadDeadline(t)
}

func (c *httpServerConn) NeedAdditionalReadDeadline() bool {
	return c.SetReadDeadline(time.Time{}) != nil
}

func (c *httpServerConn) SetWriteDeadline(t time.Time) error {
	return http.NewResponseController(c.ResponseWriter).SetWriteDeadline(t)
}

func (c *httpServerConn) Close() error {
	c.Lock()
	defer c.Unlock()
	return c.Instance.Close()
}
