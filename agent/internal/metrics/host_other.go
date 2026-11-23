//go:build !windows && !linux

package metrics

func newPlatformHost() HostCollector {
	return &mockHostCollector{}
}
