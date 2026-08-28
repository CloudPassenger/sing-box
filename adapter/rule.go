package adapter

import (
	C "github.com/sagernet/sing-box/constant"
)

type HeadlessRule interface {
	Match(metadata *InboundContext) bool
	String() string
}

type Rule interface {
	HeadlessRule
	SimpleLifecycle
	Type() string
	Action() RuleAction
}

type DNSRule interface {
	Rule
	WithAddressLimit() bool
	MatchAddressLimit(metadata *InboundContext) bool
}

type RuleAction interface {
	Type() string
	String() string
}

func IsFinalAction(action RuleAction) bool {
	if action == nil {
		return false
	}
	switch action.Type() {
	case C.RuleActionTypeSniff, C.RuleActionTypeResolve, C.RuleActionTypeRouteOptions, C.RuleActionTypeLimitOptions, C.RuleActionTypePass:
		return false
	default:
		return true
	}
}
