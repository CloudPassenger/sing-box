package rule

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

func TestLimitOptionsIsNonFinalAction(t *testing.T) {
	t.Parallel()
	action, err := NewRuleAction(context.Background(), log.NewNOPFactory().Logger(), option.RuleAction{
		Action: C.RuleActionTypeLimitOptions,
		LimitOptions: option.LimitActionOptions{
			Clients: 1,
		},
	}, C.LimitScopeRule)
	require.NoError(t, err)
	require.False(t, adapter.IsFinalAction(action))
}

func TestNewDefaultRuleInfersUserScopeForLimitOptions(t *testing.T) {
	t.Parallel()
	rule, err := NewDefaultRule(context.Background(), log.NewNOPFactory().Logger(), option.DefaultRule{
		RawDefaultRule: option.RawDefaultRule{
			User: []string{"alice"},
		},
		RuleAction: option.RuleAction{
			Action: C.RuleActionTypeLimitOptions,
			LimitOptions: option.LimitActionOptions{
				Clients: 1,
			},
		},
	})
	require.NoError(t, err)
	limitAction, loaded := rule.Action().(*RuleActionLimitOptions)
	require.True(t, loaded)
	require.Equal(t, C.LimitScopeUser, limitAction.scope)
}

func TestNewDefaultRuleInfersRuleScopeForClientLimitWithoutIdentity(t *testing.T) {
	t.Parallel()
	rule, err := NewDefaultRule(context.Background(), log.NewNOPFactory().Logger(), option.DefaultRule{
		RawDefaultRule: option.RawDefaultRule{
			DomainSuffix: []string{"example.com"},
		},
		RuleAction: option.RuleAction{
			Action: C.RuleActionTypeLimitOptions,
			LimitOptions: option.LimitActionOptions{
				Clients: 2,
			},
		},
	})
	require.NoError(t, err)
	limitAction, loaded := rule.Action().(*RuleActionLimitOptions)
	require.True(t, loaded)
	require.Equal(t, C.LimitScopeRule, limitAction.scope)
}

func TestNewDefaultRuleInfersSourceIPScopeForBandwidthOnly(t *testing.T) {
	t.Parallel()
	down := 5.0
	rule, err := NewDefaultRule(context.Background(), log.NewNOPFactory().Logger(), option.DefaultRule{
		RawDefaultRule: option.RawDefaultRule{
			DomainSuffix: []string{"example.com"},
		},
		RuleAction: option.RuleAction{
			Action: C.RuleActionTypeLimitOptions,
			LimitOptions: option.LimitActionOptions{
				DownMbps: &down,
			},
		},
	})
	require.NoError(t, err)
	limitAction, loaded := rule.Action().(*RuleActionLimitOptions)
	require.True(t, loaded)
	require.Equal(t, C.LimitScopeSourceIP, limitAction.scope)
}
