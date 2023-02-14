// Package presets ships named operator-friendly persona-mix
// configurations for the randommix.Generator. See traffic-gen/ADR-0002
// for the design rationale and the menu of shipped presets.
//
// Each preset is a value of type Preset that bundles a Name with the
// []randommix.Bias the operator gets when they pass --preset=<Name>.
// All() returns the menu in a fixed order so the cookbook and the
// --preset error message stay in sync; Lookup(name) is the cmd-side
// entry point.
//
// Operators outside the menu still have the wrapper-main path: import
// internal/traffic/randommix directly and construct your own Generator
// with custom biases. A minimal wrapper main looks like:
//
//	gen, err := randommix.New([]randommix.Bias{
//	    {Field: "country", Values: []randommix.WeightedValue{
//	        {Value: "BR", Weight: 1},
//	    }},
//	}, seed)
//
// then pass `gen` to a poster.Poster. The presets package is the
// operator-friendly shortcut, not a replacement for the lower-level
// API.
package presets
