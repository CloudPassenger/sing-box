package build_shared

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitForkTag(t *testing.T) {
	t.Parallel()

	upstreamVersion, forkVersion, isForkTag := splitForkTag("v1.13.2-superpower-0.1.1")
	require.True(t, isForkTag)
	require.Equal(t, "1.13.2", upstreamVersion)
	require.Equal(t, "0.1.1", forkVersion)

	_, _, isForkTag = splitForkTag("v1.13.2")
	require.False(t, isForkTag)

	_, _, isForkTag = splitForkTag("v1.13.2-superpower-")
	require.False(t, isForkTag)
}
