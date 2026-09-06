//go:build !windows

package main

// The desktop control window is Windows-only until the Mac release pass
// (macOS wants a real .app + WKWebView, which is cgo + bundle work that
// lands together with Developer ID signing). The dashboard covers the
// interim - same content, browser chrome.
func windowCmd() error {
	return openUI()
}
