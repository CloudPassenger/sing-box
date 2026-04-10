package limit

import (
	"context"
	"math"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/time/rate"
)

type Options struct {
	Scope          string
	Clients        uint32
	DownMbps       *float64
	UpMbps         *float64
	TotalMbps      *float64
	SamplingPeriod time.Duration
	DownBurstMbps  *float64
	UpBurstMbps    *float64
	TotalBurstMbps *float64
	RuleID         string
}

type Runtime struct {
	keyer          scopeKeyer
	clientsLimiter *clientsLimiter
	trafficLimiter *trafficLimiter
}

func NewRuntime(options Options) (*Runtime, error) {
	if options.Scope == "" {
		options.Scope = C.LimitScopeSourceIP
	}
	if options.SamplingPeriod == 0 {
		options.SamplingPeriod = time.Second
	}
	if options.SamplingPeriod < 0 {
		return nil, E.New("invalid sampling_period")
	}
	if options.Clients == 0 && options.DownMbps == nil && options.UpMbps == nil && options.TotalMbps == nil {
		return nil, E.New("missing effective limit fields")
	}
	if options.Clients > 0 && options.Scope == C.LimitScopeSourceIP {
		return nil, E.New("clients cannot be used with scope=source_ip")
	}
	keyer, err := newScopeKeyer(options.Scope, options.RuleID)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{keyer: keyer}
	if options.Clients > 0 {
		runtime.clientsLimiter = newClientsLimiter(keyer, options.Clients)
	}
	if options.DownMbps != nil || options.UpMbps != nil || options.TotalMbps != nil {
		runtime.trafficLimiter = newTrafficLimiter(keyer, options)
	}
	return runtime, nil
}

func (r *Runtime) WrapConnection(ctx context.Context, conn net.Conn, metadata *adapter.InboundContext) (net.Conn, error) {
	var err error
	if r.clientsLimiter != nil {
		conn, err = r.clientsLimiter.wrapConnection(conn, metadata)
		if err != nil {
			return nil, err
		}
	}
	if r.trafficLimiter != nil {
		conn, err = r.trafficLimiter.wrapConnection(ctx, conn, metadata)
		if err != nil {
			return nil, err
		}
	}
	return conn, nil
}

func (r *Runtime) WrapPacketConnection(ctx context.Context, conn N.PacketConn, metadata *adapter.InboundContext) (N.PacketConn, error) {
	var err error
	if r.clientsLimiter != nil {
		conn, err = r.clientsLimiter.wrapPacketConnection(conn, metadata)
		if err != nil {
			return nil, err
		}
	}
	if r.trafficLimiter != nil {
		conn, err = r.trafficLimiter.wrapPacketConnection(ctx, conn, metadata)
		if err != nil {
			return nil, err
		}
	}
	return conn, nil
}

type scopeKeyer interface {
	ScopeKey(ctx context.Context, metadata *adapter.InboundContext) (string, error)
	ClientKey(ctx context.Context, metadata *adapter.InboundContext) (string, error)
}

type defaultScopeKeyer struct {
	scope  string
	ruleID string
}

func newScopeKeyer(scope string, ruleID string) (scopeKeyer, error) {
	switch scope {
	case C.LimitScopeSourceIP, C.LimitScopeUser, C.LimitScopeInbound, C.LimitScopeRule:
		return &defaultScopeKeyer{scope: scope, ruleID: ruleID}, nil
	default:
		return nil, E.New("invalid scope: ", scope)
	}
}

func (k *defaultScopeKeyer) ScopeKey(ctx context.Context, metadata *adapter.InboundContext) (string, error) {
	switch k.scope {
	case C.LimitScopeSourceIP:
		return k.ClientKey(ctx, metadata)
	case C.LimitScopeUser:
		if metadata == nil || metadata.User == "" {
			return "", E.New("scope user requires authenticated user metadata")
		}
		return metadata.User, nil
	case C.LimitScopeInbound:
		if metadata == nil || metadata.Inbound == "" {
			return "", E.New("scope inbound requires inbound metadata")
		}
		return metadata.Inbound, nil
	case C.LimitScopeRule:
		if k.ruleID == "" {
			return "", E.New("scope rule requires stable rule identifier")
		}
		return k.ruleID, nil
	default:
		return "", E.New("invalid scope: ", k.scope)
	}
}

func (k *defaultScopeKeyer) ClientKey(ctx context.Context, metadata *adapter.InboundContext) (string, error) {
	if metadata == nil || !metadata.Source.IsIP() {
		return "", E.New("client key requires source ip")
	}
	return metadata.Source.IPAddr().String(), nil
}

type clientsLimiter struct {
	keyer   scopeKeyer
	max     uint32
	buckets map[string]map[string]uint32
	access  sync.Mutex
}

func newClientsLimiter(keyer scopeKeyer, max uint32) *clientsLimiter {
	return &clientsLimiter{keyer: keyer, max: max, buckets: make(map[string]map[string]uint32)}
}

func (l *clientsLimiter) wrapConnection(conn net.Conn, metadata *adapter.InboundContext) (net.Conn, error) {
	onClose, err := l.acquire(context.Background(), metadata)
	if err != nil {
		return nil, err
	}
	return &closeHandlerConn{ExtendedConn: bufio.NewExtendedConn(conn), onClose: onClose}, nil
}

func (l *clientsLimiter) wrapPacketConnection(conn N.PacketConn, metadata *adapter.InboundContext) (N.PacketConn, error) {
	onClose, err := l.acquire(context.Background(), metadata)
	if err != nil {
		return nil, err
	}
	return &closeHandlerPacketConn{PacketConn: conn, onClose: onClose}, nil
}

func (l *clientsLimiter) acquire(ctx context.Context, metadata *adapter.InboundContext) (func(), error) {
	scopeKey, err := l.keyer.ScopeKey(ctx, metadata)
	if err != nil {
		return nil, err
	}
	clientKey, err := l.keyer.ClientKey(ctx, metadata)
	if err != nil {
		return nil, err
	}
	l.access.Lock()
	defer l.access.Unlock()
	bucket, loaded := l.buckets[scopeKey]
	if !loaded {
		bucket = make(map[string]uint32)
		l.buckets[scopeKey] = bucket
	}
	if bucket[clientKey] == 0 && uint32(len(bucket)) >= l.max {
		return nil, E.New("client limit exceeded")
	}
	bucket[clientKey]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.access.Lock()
			defer l.access.Unlock()
			bucket := l.buckets[scopeKey]
			if bucket == nil {
				return
			}
			if bucket[clientKey] <= 1 {
				delete(bucket, clientKey)
			} else {
				bucket[clientKey]--
			}
			if len(bucket) == 0 {
				delete(l.buckets, scopeKey)
			}
		})
	}, nil
}

type limiterGroup struct {
	down    *rate.Limiter
	up      *rate.Limiter
	total   *rate.Limiter
	handles uint32
}

type trafficLimiter struct {
	keyer   scopeKeyer
	options Options
	buckets map[string]*limiterGroup
	access  sync.Mutex
}

func newTrafficLimiter(keyer scopeKeyer, options Options) *trafficLimiter {
	return &trafficLimiter{keyer: keyer, options: options, buckets: make(map[string]*limiterGroup)}
}

func (l *trafficLimiter) wrapConnection(ctx context.Context, conn net.Conn, metadata *adapter.InboundContext) (net.Conn, error) {
	group, onClose, err := l.acquire(ctx, metadata)
	if err != nil {
		return nil, err
	}
	return &trafficLimitedConn{ExtendedConn: bufio.NewExtendedConn(conn), ctx: ctx, down: group.down, up: group.up, total: group.total, onClose: onClose}, nil
}

func (l *trafficLimiter) wrapPacketConnection(ctx context.Context, conn N.PacketConn, metadata *adapter.InboundContext) (N.PacketConn, error) {
	group, onClose, err := l.acquire(ctx, metadata)
	if err != nil {
		return nil, err
	}
	return &trafficLimitedPacketConn{PacketConn: conn, ctx: ctx, down: group.down, up: group.up, total: group.total, onClose: onClose}, nil
}

func (l *trafficLimiter) acquire(ctx context.Context, metadata *adapter.InboundContext) (*limiterGroup, func(), error) {
	scopeKey, err := l.keyer.ScopeKey(ctx, metadata)
	if err != nil {
		return nil, nil, err
	}
	l.access.Lock()
	defer l.access.Unlock()
	group, loaded := l.buckets[scopeKey]
	if !loaded {
		group = &limiterGroup{
			down:  newRateLimiter(l.options.DownMbps, l.options.DownBurstMbps, l.options.SamplingPeriod),
			up:    newRateLimiter(l.options.UpMbps, l.options.UpBurstMbps, l.options.SamplingPeriod),
			total: newRateLimiter(l.options.TotalMbps, l.options.TotalBurstMbps, l.options.SamplingPeriod),
		}
		l.buckets[scopeKey] = group
	}
	group.handles++
	var once sync.Once
	return group, func() {
		once.Do(func() {
			l.access.Lock()
			defer l.access.Unlock()
			group.handles--
			if group.handles == 0 {
				delete(l.buckets, scopeKey)
			}
		})
	}, nil
}

func newRateLimiter(rateMbps *float64, burstMbps *float64, period time.Duration) *rate.Limiter {
	if rateMbps == nil {
		return nil
	}
	rateBytes := *rateMbps * C.MbpsToBps
	burstBytes := rateBytes * period.Seconds()
	if burstMbps != nil {
		burstBytes = *burstMbps * C.MbpsToBps * period.Seconds()
	}
	if burstBytes < 1 {
		burstBytes = 1
	}
	return rate.NewLimiter(rate.Limit(rateBytes), int(math.Ceil(burstBytes)))
}
