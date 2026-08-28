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

func TestNewRuleActionPassIsNonFinal(t *testing.T) {
	t.Parallel()
	action, err := NewRuleAction(context.Background(), log.NewNOPFactory().Logger(), option.RuleAction{
		Action: C.RuleActionTypePass,
	}, "")
	require.NoError(t, err)
	require.Equal(t, C.RuleActionTypePass, action.Type())
	require.Equal(t, "pass", action.String())
	require.False(t, adapter.IsFinalAction(action))
}

func TestNewDNSRuleActionPassIsNonFinal(t *testing.T) {
	t.Parallel()
	action := NewDNSRuleAction(log.NewNOPFactory().Logger(), option.DNSRuleAction{
		Action: C.RuleActionTypePass,
	})
	require.Equal(t, C.RuleActionTypePass, action.Type())
	require.Equal(t, "pass", action.String())
	require.False(t, adapter.IsFinalAction(action))
}
