package option

import (
	"context"
	"testing"

	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

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
