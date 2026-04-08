package option

import (
	"testing"

	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func TestProxyProtocolVersionUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ProxyProtocolVersion
	}{
		{name: "false", input: `{"proxy_protocol":false}`, expected: 0},
		{name: "true", input: `{"proxy_protocol":true}`, expected: 2},
		{name: "zero", input: `{"proxy_protocol":0}`, expected: 0},
		{name: "one", input: `{"proxy_protocol":1}`, expected: 1},
		{name: "two", input: `{"proxy_protocol":2}`, expected: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var options DialerOptions
			err := json.Unmarshal([]byte(test.input), &options)
			require.NoError(t, err)
			require.Equal(t, test.expected, options.ProxyProtocol)
		})
	}
}

func TestProxyProtocolVersionUnmarshalInvalid(t *testing.T) {
	var options DialerOptions
	err := json.Unmarshal([]byte(`{"proxy_protocol":3}`), &options)
	require.Error(t, err)
}
