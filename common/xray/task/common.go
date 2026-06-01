package task

import "github.com/sagernet/sing-box/common/xray"

// Close returns a func() that closes v.
func Close(v any) func() error {
	return func() error {
		return common.Close(v)
	}
}
