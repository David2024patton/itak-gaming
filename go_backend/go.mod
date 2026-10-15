module github.com/davidpatton/itak-gaming/go_backend

go 1.26.0

tool golang.org/x/mobile/cmd/gobind

require (
	github.com/davidpatton/itak-gaming/cores/go-gameboy v0.0.0
	github.com/davidpatton/itak-gaming/cores/go-gba v0.0.0
	github.com/davidpatton/itak-gaming/cores/go-genesis v0.0.0
	github.com/davidpatton/itak-gaming/cores/go-nes v0.0.0
	github.com/davidpatton/itak-gaming/cores/go-ps1 v0.0.0
	github.com/davidpatton/itak-gaming/cores/go-snes v0.0.0
	golang.org/x/mobile v0.0.0-20261011034528-197770444867 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

// The bundled cores are linked into the backend as native Go
// packages (see internal/core/native.go). Their sources live
// in this repository under cores/, so the backend replaces the
// module paths with the in-repo directories; the published
// core modules are never fetched from the network.
replace github.com/davidpatton/itak-gaming/cores/go-gameboy => ../cores/go-gameboy

replace github.com/davidpatton/itak-gaming/cores/go-gba => ../cores/go-gba

replace github.com/davidpatton/itak-gaming/cores/go-genesis => ../cores/go-genesis

replace github.com/davidpatton/itak-gaming/cores/go-nes => ../cores/go-nes

replace github.com/davidpatton/itak-gaming/cores/go-ps1 => ../cores/go-ps1

replace github.com/davidpatton/itak-gaming/cores/go-snes => ../cores/go-snes
