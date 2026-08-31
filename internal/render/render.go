// Package render turns the desired credential state into the exact file a
// gate vendor's software imports.
//
// THE RULE (from docs/GATE_INTEGRATION_PLAN.md, and it is absolute): a real
// vendor renderer is only written against a SAMPLE FILE from a live customer
// site. Vendor formats drift between versions and cannot be validated from
// documentation. Until then the registry holds exactly one renderer - the
// verification renderer - plus named stubs that fail loudly with instructions.
package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
)

// Renderer produces the vendor file's bytes from the full desired state.
type Renderer interface {
	// FileName is the default output file name for this vendor (the server's
	// settings.generatedFileName overrides it).
	FileName() string
	Render(st *api.State) ([]byte, error)
}

var registry = map[string]Renderer{}

func Register(name string, r Renderer) { registry[name] = r }

func Get(name string) (Renderer, error) {
	if r, ok := registry[name]; ok {
		return r, nil
	}
	if _, stub := stubs[name]; stub {
		return nil, fmt.Errorf(
			"provider %q is recognized but its file renderer isn't built yet - it requires a sample export from a live %s site (see internal/render/README.md)",
			name, name)
	}
	return nil, fmt.Errorf("unknown gate provider %q (known: %s)", name, strings.Join(Known(), ", "))
}

// stubs: providers we KNOW exist (the storEDGE Gate dropdown, observed
// 2026-08-30) but deliberately have no renderer until a customer site
// supplies a sample file.
var stubs = map[string]bool{
	"pti_falcon":    true, // writes e.g. C:\PTI\UPDATE.DAT, consume Ptisend.bat (PTI-MI)
	"pti_storlogix": true,
	"doorking":      true,
	"digigate":      true,
	"quikstor":      true,
	"rcs":           true, // Revenue Control Systems
	"spiderdoor":    true,
	"stor_guard":    true,
	"winsen":        true,
}

func Known() []string {
	names := make([]string, 0, len(registry)+len(stubs))
	for n := range registry {
		names = append(names, n)
	}
	for n := range stubs {
		names = append(names, n+" (stub)")
	}
	sort.Strings(names)
	return names
}
