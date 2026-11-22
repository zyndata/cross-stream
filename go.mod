module github.com/zyndata/cross-stream

go 1.26.3

require (
	github.com/coder/websocket v1.8.15
	github.com/flynn/noise v1.1.0
	github.com/google/go-tpm v0.9.8
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.47.0
	golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee
	gopkg.in/yaml.v3 v3.0.1
	gvisor.dev/gvisor v0.0.0-20260924231340-3c785112322b
)

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/google/go-tpm-tools v0.3.13-0.20230620182252-4639ecce2aba // indirect
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc // indirect
	golang.org/x/mobile v0.0.0-20261011034528-197770444867 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

tool (
	golang.org/x/mobile/cmd/gobind
	golang.org/x/mobile/cmd/gomobile
)
