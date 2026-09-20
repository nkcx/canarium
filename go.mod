module github.com/nkcx/canarium

go 1.26.0

// Pinned so every build — local, CI and container — uses the same
// compiler. go1.26.1 fixes a set of standard-library advisories
// (GO-2026-4599, GO-2026-4600 and others) that govulncheck reports against
// 1.26.0; 1.26.8 is the current patch.
toolchain go1.26.8

require (
	github.com/expr-lang/expr v1.17.8
	github.com/gorilla/websocket v1.5.3
	github.com/gosnmp/gosnmp v1.44.0
	github.com/spf13/cobra v1.10.2
	github.com/warthog618/go-gpiocdev v0.9.1
	golang.org/x/crypto v0.57.0
	golang.org/x/term v0.46.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.59.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.48.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
