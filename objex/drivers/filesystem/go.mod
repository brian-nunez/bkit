module github.com/brian-nunez/bkit/objex/drivers/filesystem

go 1.26.0

require github.com/brian-nunez/bkit/objex v0.0.0

// Temporary until objex is released; check-go-module.sh blocks a release while it is present.
replace github.com/brian-nunez/bkit/objex => ../..
