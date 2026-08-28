package option

import (
	"context"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func TestDNSRuleActionRespondUnmarshalJSON(t *testing.T) {
	t.Parallel()

	var action DNSRuleAction
	err := json.UnmarshalContext(context.Background(), []byte(`{"action":"respond"}`), &action)
	require.NoError(t, err)
	require.Equal(t, C.RuleActionTypeRespond, action.Action)
	require.Equal(t, DNSRouteActionOptions{}, action.RouteOptions)
}

func TestDNSRuleActionRespondRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	var action DNSRuleAction
	err := json.UnmarshalContext(context.Background(), []byte(`{"action":"respond","disable_cache":true}`), &action)
	require.ErrorContains(t, err, "unknown field")
}

func TestRuleActionPassJSONRoundTrip(t *testing.T) {
	t.Parallel()

	var action RuleAction
	err := json.Unmarshal([]byte(`{"action":"pass"}`), &action)
	require.NoError(t, err)
	require.Equal(t, "pass", action.Action)

	encoded, err := json.Marshal(action)
	require.NoError(t, err)
	require.Equal(t, `{"action":"pass"}`, string(encoded))
}

func TestDNSRuleActionPassJSONRoundTrip(t *testing.T) {
	t.Parallel()

	var action DNSRuleAction
	err := action.UnmarshalJSONContext(context.Background(), []byte(`{"action":"pass"}`))
	require.NoError(t, err)
	require.Equal(t, "pass", action.Action)

	encoded, err := json.Marshal(action)
	require.NoError(t, err)
	require.Equal(t, `{"action":"pass"}`, string(encoded))
}

func TestRuleActionPassRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	var action RuleAction
	err := json.Unmarshal([]byte(`{"action":"pass","outbound":"x"}`), &action)
	require.Error(t, err)
}
